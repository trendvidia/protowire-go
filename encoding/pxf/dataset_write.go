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
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// AppendDatasetHeader appends the header `@dataset <type> (<columns>)`
// for rows of type desc, and a newline, to dst.
//
// Any field of desc can be a column: a cell may hold a list or a block
// (protowire#341). A column may also be a path into a sub-message, such
// as `inner.x` (protowire#342), through singular message fields only. It
// returns an error, and dst unchanged, for an empty column list and for
// a column the reader would refuse: a name that is not a field, a path
// with an empty segment, a column twice, a field beside a path inside it
// (`inner` with `inner.x`), or a path through a repeated or map field or
// into a type written as a literal. A (pxf.required) field of desc that
// is neither a column nor under one is an error too: every row would be
// rejected on reading. So is a schema that
// [ValidateDescriptor] refuses, which Unmarshal would refuse to read.
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
// A dotted column is written through its parents: an unset parent makes
// the cell empty, and the leaf follows the rules above inside its
// parent. A sub-message is present on reading exactly when a cell under
// it is non-empty (protowire#342), so one that is set but has every
// column under it empty reads back unset; that loss is accepted, as for
// a field outside the columns. A row that the reader would reject for
// it is refused instead: a required sub-message whose cells are all
// empty, or a sub-message that reads back present while one of its
// required fields is not a column.
//
// Fields not among the columns are not written, so a table can export a
// subset of a message's fields. They read back absent, taking their
// (pxf.default) if they have one. A (pxf.required) field cannot be left
// out: the column list is refused.
//
// A list, map or message that needs a block is written as Marshal writes
// it, on one line, so a table keeps one row per line. Inside it, an
// unset field with (pxf.default) or (pxf.required) follows the rules
// above, since the reader applies both in every body of a cell (#161):
// a scalar without presence is written as its zero value, a
// message-typed field `null`, an `optional` or oneof scalar not at all
// (it reads back as its default), and an unset required field with
// presence is an error.
//
// It returns an error, and dst unchanged, for the column errors of
// AppendDatasetHeader and for a value Marshal refuses.
func AppendDatasetRow(dst []byte, msg proto.Message, columns []string) ([]byte, error) {
	m := msg.ProtoReflect()
	desc := m.Descriptor()
	cols, err := datasetColumns(desc, columns)
	if err != nil {
		return dst, err
	}
	buf := bytes.NewBuffer(dst)
	enc := &encoder{buf: buf}
	if nfd := findNullMaskField(desc); nfd != nil && m.Has(nfd) {
		enc.nullSet = readNullMask(m, nfd)
	}
	filled := make(map[string]bool) // path prefixes with a non-empty cell
	buf.WriteByte('(')
	for i, c := range cols {
		if i > 0 {
			buf.WriteString(", ")
		}
		parent, ok := c.parentIn(m)
		if !ok {
			continue // a parent is unset: an empty cell, and it reads back unset
		}
		leaf := c.path[len(c.path)-1]
		before := buf.Len()
		opts := datasetCellOptions(parent.Descriptor())
		if err := enc.writeCell(parent, leaf, c.name, c.siblings, opts[leaf.Index()]); err != nil {
			return dst, fmt.Errorf("pxf: @dataset column %q: %w", c.name, err)
		}
		if buf.Len() > before {
			for _, p := range c.prefixes() {
				filled[p] = true
			}
		}
	}
	buf.WriteString(")\n")
	if err := checkRowPresence(m, cols, filled); err != nil {
		return dst, err
	}
	return buf.Bytes(), nil
}

// datasetColumn is one resolved column: its name as written and the
// fields from the row type down to the leaf (one for a plain column).
type datasetColumn struct {
	name     string
	path     []protoreflect.FieldDescriptor
	siblings []protoreflect.FieldDescriptor // leaves of the columns with the same parent, this one included
}

// parentIn walks c's path in m to the message holding the leaf. It
// reports false when a parent on the way is unset.
func (c datasetColumn) parentIn(m protoreflect.Message) (protoreflect.Message, bool) {
	for _, fd := range c.path[:len(c.path)-1] {
		if !m.Has(fd) {
			return nil, false
		}
		m = m.Get(fd).Message()
	}
	return m, true
}

// prefixes is the dotted names of c's parents: "a" and "a.b" for "a.b.c".
func (c datasetColumn) prefixes() []string {
	segs := strings.Split(c.name, ".")
	out := make([]string, 0, len(segs)-1)
	for k := 1; k < len(segs); k++ {
		out = append(out, strings.Join(segs[:k], "."))
	}
	return out
}

// checkRowPresence refuses a row that the reader would reject for a
// missing (pxf.required) field below the root, the one way a dotted
// column can make a row unreadable. A sub-message is present on reading
// exactly when one of the cells under it is non-empty (draft §3.4.4,
// protowire#342 decision 1). So a sub-message that is present in msg but
// whose cells all come out empty reads back absent: that loss is
// accepted (owner decision on #155) unless the sub-message is required,
// and then the row is refused. And a sub-message that reads back present
// needs each of its required fields as a column, or under one.
func checkRowPresence(m protoreflect.Message, cols []datasetColumn, filled map[string]bool) error {
	covered := func(path string) (exact, prefix bool) {
		for _, c := range cols {
			if c.name == path {
				return true, false
			}
			if strings.HasPrefix(c.name, path+".") {
				prefix = true
			}
		}
		return false, prefix
	}
	var check func(md protoreflect.MessageDescriptor, at string) error
	check = func(md protoreflect.MessageDescriptor, at string) error {
		opts := datasetCellOptions(md)
		fields := md.Fields()
		for i := range fields.Len() {
			fd := fields.Get(i)
			path := string(fd.Name())
			if at != "" {
				path = at + "." + path
			}
			exact, prefix := covered(path)
			if prefix && !filled[path] && opts[i].required {
				return fmt.Errorf("pxf: @dataset columns under %q are all empty in this row, so it reads back unset, and it is (pxf.required)", path)
			}
			if at != "" && opts[i].required && !exact && !prefix {
				return fmt.Errorf("pxf: @dataset %q reads back present, and its (pxf.required) field %q is not a column: the row would be rejected", at, path)
			}
			if prefix && filled[path] {
				if err := check(fd.Message(), path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(m.Descriptor(), "")
}

// writeCell writes the cell for fd of m, in a row of the columns cols;
// see AppendDatasetRow.
func (e *encoder) writeCell(m protoreflect.Message, fd protoreflect.FieldDescriptor, path string, cols []protoreflect.FieldDescriptor, o cellOptions) error {
	isMsg := fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind
	if isMsg && e.nullSet[path] {
		e.buf.WriteString("null")
		return nil
	}
	if m.Has(fd) {
		switch {
		case fd.IsList() || fd.IsMap():
			return e.writeValueCell(fd, m.Get(fd), nil)
		case !isMsg:
			return e.writeScalar(fd, m.Get(fd))
		}
		sub := m.Get(fd).Message()
		if !isLiteralMessage(sub) {
			return e.writeValueCell(fd, protoreflect.Value{}, sub)
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

// cellUnset decides how a value cell writes fd, the i'th field of msg,
// which msg does not set. Inside a cell, as in a row, the reader applies
// (pxf.default) to an absent field and checks (pxf.required) (#161), so
// an unset annotated field is written the way writeCell writes one: a
// field without presence as its zero value (write), a message-typed
// field as null, and an optional or oneof scalar not at all, which reads
// back as its default. A oneof member is not written when a sibling is
// set: the sibling keeps its default off. An unset required field with
// presence cannot be written. An unannotated field is not written.
func cellUnset(msg protoreflect.Message, fd protoreflect.FieldDescriptor, i int, path string) (write, null bool, err error) {
	o := datasetCellOptions(msg.Descriptor())[i]
	switch {
	case !o.hasDefault && !o.required:
		return false, false, nil
	case !fd.HasPresence():
		return true, false, nil
	case o.required:
		return false, false, fmt.Errorf("%s is unset, and the field is (pxf.required)", path)
	}
	if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() && msg.WhichOneof(od) != nil {
		return false, false, nil
	}
	isMsg := fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind
	return false, isMsg, nil
}

// writeValueCell writes a list, map or block value as one cell
// (protowire#341): the text Marshal writes for the field, on one line, so
// a table keeps one row per line. The field is encoded as Marshal
// encodes it at no indent, the field name that leads it is cut, and its
// newlines become spaces. That is safe because Marshal never writes a raw
// newline inside a token (strings escape it, bytes are base64), so every
// newline it writes separates tokens.
func (e *encoder) writeValueCell(fd protoreflect.FieldDescriptor, v protoreflect.Value, sub protoreflect.Message) error {
	var tmp bytes.Buffer
	inner := &encoder{buf: &tmp, nullSet: e.nullSet, cellBodies: true}
	var err error
	switch {
	case fd.IsMap():
		err = inner.encodeMapField(fd, v, 0)
	case fd.IsList():
		err = inner.encodeListField(fd, v, 0)
	default:
		err = inner.encodeMessageField(fd, sub, 0)
	}
	if err != nil {
		return err
	}
	out := tmp.Bytes()
	out = bytes.TrimPrefix(out, []byte(fd.Name()))
	out = bytes.TrimPrefix(out, []byte(" = "))
	out = bytes.TrimPrefix(out, []byte(" "))
	out = bytes.TrimSuffix(out, []byte("\n"))
	for i, ch := range out {
		if ch == '\n' {
			out[i] = ' '
		}
	}
	e.buf.Write(out)
	return nil
}

// datasetColumns resolves columns against desc, refusing what a v1 row
// cannot carry; see AppendDatasetHeader. It also returns desc's
// cellOptions.
func datasetColumns(desc protoreflect.MessageDescriptor, columns []string) ([]datasetColumn, error) {
	if len(columns) == 0 {
		return nil, errors.New("pxf: @dataset needs at least one column")
	}
	// A schema the reader would refuse is refused here instead of
	// written: a field with a reserved name makes the header unreadable,
	// and a oneof with two defaulted members would get two null cells.
	if err := asValidationError(ValidateDescriptor(desc)); err != nil {
		return nil, fmt.Errorf("pxf: @dataset %s: %w", desc.FullName(), err)
	}
	// The reader's rules for column paths (protowire#342), with its
	// messages, so the writer cannot write a header its reader rejects:
	// no empty segment, no path twice, no field beside a path inside it,
	// and each path through singular message fields only.
	if msg := checkColumnPaths(desc, columns); msg != "" {
		return nil, errors.New("pxf: " + msg)
	}
	cols := make([]datasetColumn, len(columns))
	for i, c := range columns {
		md := desc
		segs := strings.Split(c, ".")
		path := make([]protoreflect.FieldDescriptor, len(segs))
		for k, seg := range segs {
			// checkColumnPaths resolved every segment.
			fd := md.Fields().ByName(protoreflect.Name(seg))
			path[k] = fd
			if k < len(segs)-1 {
				md = fd.Message()
			}
		}
		cols[i] = datasetColumn{name: c, path: path}
	}
	for i := range cols {
		for _, o := range cols {
			if len(o.path) == len(cols[i].path) && slices.Equal(o.path[:len(o.path)-1], cols[i].path[:len(cols[i].path)-1]) {
				cols[i].siblings = append(cols[i].siblings, o.path[len(o.path)-1])
			}
		}
	}
	opts := datasetCellOptions(desc)
	for i, o := range opts {
		fd := desc.Fields().Get(i)
		if !o.required {
			continue
		}
		if !slices.ContainsFunc(cols, func(c datasetColumn) bool { return c.path[0] == fd }) {
			return nil, fmt.Errorf("pxf: @dataset columns omit %q, which is (pxf.required): every row would be rejected", fd.Name())
		}
	}
	return cols, nil
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
