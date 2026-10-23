// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Writing `@dataset` tables (draft §3.4.4): the header and one row per
// message. Cells are rendered by the encoder Marshal uses, so a cell
// reads back exactly as the same field would in a Marshal document.
//
// A row depends only on its message and the column list, never on the
// rows before it, so appending rows to a file that already holds the
// header needs no rewrite of earlier bytes.

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// AppendDatasetHeader appends the header `@dataset <type> (<columns>)`
// for rows of type desc, and a newline, to dst.
//
// It returns an error, and dst unchanged, when the columns are not ones
// a v1 row can carry: an empty list, a name that is dotted, repeated, or
// not a field of desc, or a field whose value no v1 cell can hold (a
// repeated or map field, or a message other than a Timestamp, Duration,
// wrapper, pxf.BigInt, pxf.Decimal, pxf.BigFloat or pxf.Secret). A
// (pxf.required) field missing from the columns is an error too: every
// row would be rejected on reading. So is a schema that
// [ValidateDescriptor] refuses, which Unmarshal would refuse to read.
func AppendDatasetHeader(dst []byte, desc protoreflect.MessageDescriptor, columns []string) ([]byte, error) {
	if _, _, err := datasetColumns(desc, columns); err != nil {
		return dst, err
	}
	dst = append(dst, "@dataset "...)
	dst = append(dst, desc.FullName()...)
	dst = append(dst, " ("...)
	for i, c := range columns {
		if i > 0 {
			dst = append(dst, ", "...)
		}
		dst = append(dst, c...)
	}
	return append(dst, ")\n"...), nil
}

// AppendDatasetRow appends msg as one row of a `@dataset` table with
// these columns, and a newline, to dst. Use the columns given to
// [AppendDatasetHeader].
//
// A set field is written as Marshal writes its value. An unset field
// is an empty cell, which reads back as absent, except where reading
// an empty cell would not give back the unset field:
//
//   - A field with (pxf.default) would take the default. A proto3
//     scalar without presence is written as its zero value, which the
//     default does not replace. A message-typed field (a wrapper, a
//     Timestamp, ...) is written `null`, which keeps the default off
//     and leaves the field unset. A scalar with presence (`optional`,
//     or a oneof member) stays empty and reads back as its default:
//     the draft rejects `null` on a scalar field, so no cell holds it
//     unset, and an absent field with a default means the default.
//     A oneof member stays empty when another member of its oneof is
//     set and among the columns, since that member keeps the default
//     off. A set member that is not a column is not written, so the
//     oneof is treated as having no member set.
//   - A (pxf.required) field would be rejected. A proto3 scalar is
//     written as its zero value; an unset field with presence is an
//     error.
//
// A message-typed field named in the message's `_null` FieldMask is
// written `null`, as Marshal writes it. A scalar field named there is
// written as if the mask did not name it, since the draft rejects
// `null` on a scalar field (trendvidia/protowire#339).
//
// Fields not among the columns are not written, so a table can export a
// subset of a message's fields. They read back absent, taking their
// (pxf.default) if they have one. A (pxf.required) field cannot be left
// out: the column list is refused.
//
// It returns an error, and dst unchanged, for the column errors of
// AppendDatasetHeader, for a pxf.Secret carrying a hint or fingerprint
// (which needs a block, not a cell), and for a value Marshal refuses.
func AppendDatasetRow(dst []byte, msg proto.Message, columns []string) ([]byte, error) {
	m := msg.ProtoReflect()
	desc := m.Descriptor()
	fds, opts, err := datasetColumns(desc, columns)
	if err != nil {
		return dst, err
	}
	buf := bytes.NewBuffer(dst)
	enc := &encoder{buf: buf}
	if nfd := findNullMaskField(desc); nfd != nil && m.Has(nfd) {
		enc.nullSet = readNullMask(m, nfd)
	}
	buf.WriteByte('(')
	for i, fd := range fds {
		if i > 0 {
			buf.WriteString(", ")
		}
		if err := enc.writeCell(m, fd, fds, opts[fd.Index()]); err != nil {
			return dst, fmt.Errorf("pxf: @dataset column %q: %w", fd.Name(), err)
		}
	}
	buf.WriteString(")\n")
	return buf.Bytes(), nil
}

// writeCell writes the cell for fd of m, in a row of the columns cols;
// see AppendDatasetRow.
func (e *encoder) writeCell(m protoreflect.Message, fd protoreflect.FieldDescriptor, cols []protoreflect.FieldDescriptor, o cellOptions) error {
	isMsg := fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind
	if isMsg && e.nullSet[string(fd.Name())] {
		e.buf.WriteString("null")
		return nil
	}
	if m.Has(fd) {
		if !isMsg {
			return e.writeScalar(fd, m.Get(fd))
		}
		sub := m.Get(fd).Message()
		if !isLiteralMessage(sub) {
			return errors.New("a pxf.Secret with a hint or fingerprint needs a block; v1 cells hold literals only")
		}
		return e.writeLiteralMessage(sub)
	}
	if !o.hasDefault && !o.required {
		return nil // empty: absent, and absent is what it reads back as
	}
	if !fd.HasPresence() {
		return e.writeScalar(fd, m.Get(fd)) // the zero value, explicitly
	}
	if o.required {
		return errors.New("unset, and the field is (pxf.required)")
	}
	if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
		if set := m.WhichOneof(od); set != nil && slices.Contains(cols, set) {
			// A member written in this row keeps the default off. (At
			// most one member of a oneof may carry a default, which
			// datasetColumns has checked, so no second null can arise.)
			return nil
		}
	}
	if !isMsg {
		// No cell holds an unset scalar with presence: the draft rejects
		// null on a scalar field. Empty reads back as the default.
		return nil
	}
	e.buf.WriteString("null")
	return nil
}

// datasetColumns resolves columns against desc, refusing what a v1 row
// cannot carry; see AppendDatasetHeader. It also returns desc's
// cellOptions.
func datasetColumns(desc protoreflect.MessageDescriptor, columns []string) ([]protoreflect.FieldDescriptor, []cellOptions, error) {
	if len(columns) == 0 {
		return nil, nil, errors.New("pxf: @dataset needs at least one column")
	}
	// The reader parses rows through Unmarshal with SkipValidate, so a
	// schema it would refuse is refused here instead of written: a field
	// with a reserved name makes the header unreadable, and a oneof with
	// two defaulted members would get two null cells.
	if err := asValidationError(ValidateDescriptor(desc)); err != nil {
		return nil, nil, fmt.Errorf("pxf: @dataset %s: %w", desc.FullName(), err)
	}
	fds := make([]protoreflect.FieldDescriptor, len(columns))
	for i, c := range columns {
		if containsDot(c) {
			return nil, nil, fmt.Errorf("pxf: @dataset column %q: dotted column paths are not supported in v1 (draft §3.4.4)", c)
		}
		fd := desc.Fields().ByName(protoreflect.Name(c))
		if fd == nil {
			return nil, nil, fmt.Errorf("pxf: @dataset column %q: %s has no such field", c, desc.FullName())
		}
		if slices.Contains(fds[:i], fd) {
			return nil, nil, fmt.Errorf("pxf: @dataset column %q appears twice", c)
		}
		switch {
		case fd.IsMap():
			return nil, nil, fmt.Errorf("pxf: @dataset column %q: a map field cannot be held by a v1 cell (draft §3.4.4)", c)
		case fd.IsList():
			return nil, nil, fmt.Errorf("pxf: @dataset column %q: a repeated field cannot be held by a v1 cell (draft §3.4.4)", c)
		case (fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind) && !isLiteralMessageType(fd.Message()):
			return nil, nil, fmt.Errorf("pxf: @dataset column %q: a %s field needs a block, which a v1 cell cannot hold (draft §3.4.4)", c, fd.Message().FullName())
		}
		fds[i] = fd
	}
	opts := datasetCellOptions(desc)
	for i, o := range opts {
		if fd := desc.Fields().Get(i); o.required && !slices.Contains(fds, fd) {
			return nil, nil, fmt.Errorf("pxf: @dataset columns omit %q, which is (pxf.required): every row would be rejected", fd.Name())
		}
	}
	return fds, opts, nil
}

// cellOptions is what a row needs of a field's PXF annotations.
type cellOptions struct {
	hasDefault, required bool
}

// cellOptionsCache memoizes datasetCellOptions per message descriptor,
// bounded like the bind-time validation caches (see
// maxCachedDescriptors). Every row of a table needs the options of every
// field, for the required-column check, and parsing them was a third
// of a row's cost.
var (
	cellOptionsCache sync.Map // protoreflect.MessageDescriptor -> []cellOptions
	cellOptionsCount atomic.Int64
)

// datasetCellOptions returns the cellOptions of desc's fields, indexed
// like desc.Fields(). The result is shared and must not be modified.
func datasetCellOptions(desc protoreflect.MessageDescriptor) []cellOptions {
	// A sync.Map panics on an unhashable key; see loadCached.
	cacheable := reflect.TypeOf(desc).Comparable()
	if cacheable {
		if v, hit := cellOptionsCache.Load(desc); hit {
			return v.([]cellOptions)
		}
	}
	fields := desc.Fields()
	out := make([]cellOptions, fields.Len())
	for i := range out {
		o := pxfFieldOptions(fields.Get(i))
		out[i] = cellOptions{hasDefault: o.defSet, required: o.required}
	}
	if cacheable && cellOptionsCount.Load() < maxCachedDescriptors {
		if _, loaded := cellOptionsCache.LoadOrStore(desc, out); !loaded {
			cellOptionsCount.Add(1)
		}
	}
	return out
}
