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
// row would be rejected on reading.
func AppendDatasetHeader(dst []byte, desc protoreflect.MessageDescriptor, columns []string) ([]byte, error) {
	if _, err := datasetColumns(desc, columns); err != nil {
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
//     or a oneof member) is an error: no cell holds it, because the
//     draft rejects `null` on a scalar field (trendvidia/protowire#339
//     asks whether it should). A oneof member stays empty when another
//     member of its oneof is set, since that keeps the default off.
//   - A (pxf.required) field would be rejected. A proto3 scalar is
//     written as its zero value; an unset field with presence is an
//     error.
//
// Fields not among the columns are not written, and read back absent.
//
// It returns an error, and dst unchanged, for the column errors of
// AppendDatasetHeader, for a pxf.Secret carrying a hint or fingerprint
// (which needs a block, not a cell), and for a value Marshal refuses.
func AppendDatasetRow(dst []byte, msg proto.Message, columns []string) ([]byte, error) {
	m := msg.ProtoReflect()
	fds, err := datasetColumns(m.Descriptor(), columns)
	if err != nil {
		return dst, err
	}
	buf := bytes.NewBuffer(dst)
	enc := &encoder{buf: buf}
	buf.WriteByte('(')
	for i, fd := range fds {
		if i > 0 {
			buf.WriteString(", ")
		}
		if err := enc.writeCell(m, fd); err != nil {
			return dst, fmt.Errorf("pxf: @dataset column %q: %w", fd.Name(), err)
		}
	}
	buf.WriteString(")\n")
	return buf.Bytes(), nil
}

// writeCell writes the cell for fd of m; see AppendDatasetRow.
func (e *encoder) writeCell(m protoreflect.Message, fd protoreflect.FieldDescriptor) error {
	if m.Has(fd) {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return e.writeScalar(fd, m.Get(fd))
		}
		sub := m.Get(fd).Message()
		if !isLiteralMessage(sub) {
			return errors.New("a pxf.Secret with a hint or fingerprint needs a block; v1 cells hold literals only")
		}
		return e.writeLiteralMessage(sub)
	}
	_, hasDefault := Default(fd)
	required := IsRequired(fd)
	if !hasDefault && !required {
		return nil // empty: absent, and absent is what it reads back as
	}
	if !fd.HasPresence() {
		return e.writeScalar(fd, m.Get(fd)) // the zero value, explicitly
	}
	if required {
		return errors.New("unset, and the field is (pxf.required)")
	}
	if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() && m.WhichOneof(od) != nil {
		// A set member keeps the default off. (At most one member of a
		// oneof may carry a default, so no second null can arise.)
		return nil
	}
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return errors.New("unset, and no cell holds that: an empty cell reads back as the field's (pxf.default), and the draft rejects null on a scalar field")
	}
	e.buf.WriteString("null")
	return nil
}

// datasetColumns resolves columns against desc, refusing what a v1 row
// cannot carry; see AppendDatasetHeader.
func datasetColumns(desc protoreflect.MessageDescriptor, columns []string) ([]protoreflect.FieldDescriptor, error) {
	if len(columns) == 0 {
		return nil, errors.New("pxf: @dataset needs at least one column")
	}
	fds := make([]protoreflect.FieldDescriptor, len(columns))
	for i, c := range columns {
		if containsDot(c) {
			return nil, fmt.Errorf("pxf: @dataset column %q: dotted column paths are not supported in v1 (draft §3.4.4)", c)
		}
		fd := desc.Fields().ByName(protoreflect.Name(c))
		if fd == nil {
			return nil, fmt.Errorf("pxf: @dataset column %q: %s has no such field", c, desc.FullName())
		}
		for _, prev := range fds[:i] {
			if prev == fd {
				return nil, fmt.Errorf("pxf: @dataset column %q appears twice", c)
			}
		}
		switch {
		case fd.IsMap():
			return nil, fmt.Errorf("pxf: @dataset column %q: a map field cannot be held by a v1 cell (draft §3.4.4)", c)
		case fd.IsList():
			return nil, fmt.Errorf("pxf: @dataset column %q: a repeated field cannot be held by a v1 cell (draft §3.4.4)", c)
		case (fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind) && !isLiteralMessageType(fd.Message()):
			return nil, fmt.Errorf("pxf: @dataset column %q: a %s field needs a block, which a v1 cell cannot hold (draft §3.4.4)", c, fd.Message().FullName())
		}
		fds[i] = fd
	}
	all := desc.Fields()
	for i := range all.Len() {
		fd := all.Get(i)
		if !IsRequired(fd) {
			continue
		}
		found := false
		for _, c := range fds {
			if c == fd {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("pxf: @dataset columns omit %q, which is (pxf.required): every row would be rejected", fd.Name())
		}
	}
	return fds, nil
}
