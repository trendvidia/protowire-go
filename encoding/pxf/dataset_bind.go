// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Per-row proto binding for @dataset rows. Sits atop the streaming
// DatasetReader (DatasetReader.Scan) and is also exported as a standalone
// helper (BindRow) for callers that iterate the materializing path's
// Result.Datasets()[i].Rows.
//
// Implementation strategy: convert each non-nil cell back to its PXF
// text representation, concatenate as a `<column> = <value>\n` body
// (with dotted columns grouped into blocks, see [rowToPXFBody]), and run through the existing decoder: Unmarshal, or UnmarshalFull for
// a row type that needs presence (see [rowNeedsPresence]). That reuses every
// branch of the existing decoder — WKT timestamps and durations,
// wrapper-type nullability, enum-by-name resolution, pxf.required /
// pxf.default, oneof handling — instead of growing a parallel
// Value-to-FieldDescriptor switch with ~50 arms. The cost is a small
// format + reparse per row; that's an acceptable trade for a row-
// streaming API whose consumers have already opted into the
// convenience tier.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Scan reads the next row and binds its cells to the matching fields
// of msg by column name. Returns [io.EOF] when the row sequence is
// exhausted; returns the same sticky error as [DatasetReader.Next] on
// any read or parse failure.
//
// msg's descriptor MUST resolve the field names this reader's
// Columns() list refers to. Type compatibility against the @dataset
// header's declared type is the caller's responsibility — a row
// whose columns don't match msg's fields surfaces as a per-field
// "field not found" or type-mismatch error from the underlying
// Unmarshal call.
//
// Cell-state semantics (mirrors draft §3.4.4):
//
//   - nil Value (empty cell) — field absent. (pxf.default) is
//     applied if declared on the field; (pxf.required) errors if
//     neither default nor value is present. A field that is not a
//     column is absent in the same way.
//   - *NullVal — field present-but-null: it stays unset, satisfies
//     (pxf.required) and keeps (pxf.default) off, and its name is
//     added to msg's _null FieldMask when the row type declares one.
//     The draft rejects null on a scalar field that is not a wrapper;
//     this decoder accepts it, as it does outside @dataset
//     (trendvidia/protowire#339).
//   - any other Value — field set to that value.
//
// msg is not reset first, so "stays unset" and "absent" describe a
// fresh message: bind each row into a new or [proto.Reset] message. In
// a reused one, a field an earlier row set keeps its value under a null
// cell, and a _null mask grows by every row's nulls.
//
// msg's descriptor is checked against the bind-time schema rules
// ([ValidateDescriptor]), as [Unmarshal] checks it, and a violation
// fails the row. A row's decode error carries the row's position.
func (tr *DatasetReader) Scan(msg proto.Message) error {
	row, err := tr.Next()
	if err != nil {
		return err
	}
	// The reader's limits, not MaxMessageSize: the row's bytes were
	// capped as they were read, and the synthetic body may format wider.
	o := UnmarshalOptions{
		MaxNestingDepth:         tr.lim.maxDepth,
		MaxNumericLiteralDigits: tr.lim.maxDigits,
		MaxBytesLiteralLength:   tr.lim.maxBytesLiteral,
		MaxRepeatedCount:        tr.lim.maxRepeated,
	}
	return o.bindRow(msg, tr.columns, row)
}

// BindRow binds row.Cells to the fields of msg by column name. The
// columns slice MUST have the same length as row.Cells; a mismatch is
// a programmer error and returns an error.
//
// Exported so callers iterating the materializing path's
// Result.Datasets()[i].Rows can reuse the same logic. Same cell-state
// semantics as [DatasetReader.Scan].
func BindRow(msg proto.Message, columns []string, row DatasetRow) error {
	return UnmarshalOptions{}.bindRow(msg, columns, row)
}

// bindRow is [BindRow] under the options' limits. The bind-time schema
// checks run unless the caller set SkipValidate.
func (o UnmarshalOptions) bindRow(msg proto.Message, columns []string, row DatasetRow) error {
	if len(columns) != len(row.Cells) {
		return fmt.Errorf("pxf: BindRow: %d columns vs %d cells", len(columns), len(row.Cells))
	}
	desc := msg.ProtoReflect().Descriptor()
	if err := checkColumnPaths(desc, columns); err != nil {
		return err
	}
	body, spans, err := rowToPXFBody(columns, row)
	if err != nil {
		return err
	}
	// Both paths run the bind-time schema checks, as Unmarshal does by
	// default: they are memoized per descriptor, so a row pays a cache
	// lookup for them, and they are what make the annotations
	// well-defined (two spellings of one default that disagree,
	// (pxf.required) on a oneof member).
	if !rowNeedsPresence(desc) {
		return atRow(o.Unmarshal(body, msg), row.Pos, columns, spans)
	}
	// The full pipeline's post-decode pass gives an empty cell (no
	// entry) the field's (pxf.default) and rejects it for
	// (pxf.required), and lets a null cell satisfy required and keep the
	// default off (draft §3.4.4); it also records a null cell in the
	// row type's _null mask. It costs a presence record per row, so only
	// a row type that needs it pays for it.
	_, err = o.UnmarshalFull(body, msg)
	return atRow(err, row.Pos, columns, spans)
}

// atRow moves a decode error to the row it came from, and names the
// column whose entry it fell in. The decoder reads a synthetic body, so
// its positions are lines of that body; spans says which column each
// line belongs to. A required field's error has no position at all
// (1:1), which is the body's empty first line, so it names no column. A
// row built by hand has no position to give, and keeps the body's.
func atRow(err error, pos Position, columns []string, spans []cellSpan) error {
	pe, ok := err.(*Error)
	if !ok {
		return err
	}
	msg := pe.Msg
	for _, sp := range spans {
		if pe.Pos.Line >= sp.first && pe.Pos.Line <= sp.last {
			msg = "column " + columns[sp.col] + ": " + msg
			break
		}
	}
	if pos.Line == 0 {
		pos = pe.Pos
	}
	return &Error{Pos: pos, Msg: msg}
}

// Row types [rowNeedsPresence] has answered for, memoized because
// BindRow asks once per row and the answer reads every field's options.
// Bounded like the schema-check caches, for the same reason (see
// [maxCachedDescriptors]).
var (
	rowPresenceCache sync.Map // protoreflect.MessageDescriptor -> bool
	rowPresenceCount atomic.Int64
)

// rowNeedsPresence reports whether a row of type desc must go through the
// full pipeline: a field carries (pxf.default) or (pxf.required), or desc
// declares a _null mask. A block cell fills a sub-message, and the
// post-decode pass recurses into present singular sub-messages, so their
// fields count too.
func rowNeedsPresence(desc protoreflect.MessageDescriptor) bool {
	cacheable := reflect.TypeOf(desc).Comparable()
	if cacheable {
		if v, hit := rowPresenceCache.Load(desc); hit {
			return v.(bool)
		}
	}
	need := findNullMaskField(desc) != nil || hasDefaultOrRequired(desc, map[protoreflect.FullName]bool{})
	if cacheable && rowPresenceCount.Load() < maxCachedDescriptors {
		if _, loaded := rowPresenceCache.LoadOrStore(desc, need); !loaded {
			rowPresenceCount.Add(1)
		}
	}
	return need
}

// hasDefaultOrRequired reports whether a field of desc, or of a message
// reachable from desc through singular message fields, carries
// (pxf.default) or (pxf.required): the fields postDecode visits. A block
// cell (protowire#341) fills such a sub-message. Repeated and map fields
// are not visited, as postDecode does not visit them. seen guards
// recursive types.
func hasDefaultOrRequired(desc protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) bool {
	if seen[desc.FullName()] {
		return false
	}
	seen[desc.FullName()] = true
	fields := desc.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if o := pxfFieldOptions(fd); o.defSet || o.required {
			return true
		}
		if (fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind) &&
			!fd.IsList() && !fd.IsMap() && hasDefaultOrRequired(fd.Message(), seen) {
			return true
		}
	}
	return false
}

// cellSpan is the lines of a synthetic row body that hold the entry of
// column col.
type cellSpan struct{ col, first, last int }

// bodyNode is one entry of a row body: a column's cell, or the block of
// a path prefix that dotted columns share.
type bodyNode struct {
	name     string
	col      int // the column whose cell this is; -1 for a block
	children []*bodyNode
}

// firstCol is the column of the first cell inside block n.
func firstCol(n *bodyNode) int {
	for n.col < 0 {
		n = n.children[0]
	}
	return n.col
}

// rowToPXFBody renders a row as a PXF body. A non-empty cell in column
// "c" is the entry `c = <value>`. One in a dotted column "a.b.c" is the
// entry `c = <value>` inside `a { b { ... } }`, and every cell whose
// column starts with "a." lands in that same one `a` block, in the order
// its first non-empty cell appears (draft §3.4.4). Two blocks for one
// sub-message would merge, but two for one oneof member are an error.
// Empty cells produce no entry, and a block none of whose cells is
// non-empty is not written: the field stays absent.
//
// The body opens with an empty line, so an error the decoder reports at
// 1:1 (a missing required field) falls in no column's span.
func rowToPXFBody(columns []string, row DatasetRow) ([]byte, []cellSpan, error) {
	root := &bodyNode{col: -1}
	for i, cell := range row.Cells {
		if cell == nil {
			continue
		}
		n := root
		segs := strings.Split(columns[i], ".")
		for _, seg := range segs[:len(segs)-1] {
			var next *bodyNode
			for _, ch := range n.children {
				if ch.col < 0 && ch.name == seg {
					next = ch
					break
				}
			}
			if next == nil {
				next = &bodyNode{name: seg, col: -1}
				n.children = append(n.children, next)
			}
			n = next
		}
		n.children = append(n.children, &bodyNode{name: segs[len(segs)-1], col: i})
	}
	buf := bytes.NewBuffer([]byte{'\n'})
	line := 2
	var spans []cellSpan
	var write func(n *bodyNode) error
	write = func(n *bodyNode) error {
		for _, ch := range n.children {
			if ch.col < 0 {
				// An error on the block's own lines (a second oneof
				// member, say) is laid at the first column in it. Its
				// span comes after its cells', so a cell's wins.
				first := line
				buf.WriteString(ch.name)
				buf.WriteString(" {\n")
				line++
				if err := write(ch); err != nil {
					return err
				}
				buf.WriteString("}\n")
				spans = append(spans, cellSpan{col: firstCol(ch), first: first, last: line})
				line++
				continue
			}
			start := buf.Len()
			buf.WriteString(ch.name)
			buf.WriteString(" = ")
			if err := writeCellValue(buf, row.Cells[ch.col]); err != nil {
				return err
			}
			buf.WriteByte('\n')
			last := line + bytes.Count(buf.Bytes()[start:], []byte{'\n'}) - 1
			spans = append(spans, cellSpan{col: ch.col, first: line, last: last})
			line = last + 1
		}
		return nil
	}
	if err := write(root); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), spans, nil
}

// writeCellValue formats a single cell value as PXF text. A list or block
// cell is written with the package formatter, so the synthetic body binds
// it exactly as `<column> = <value>` would bind in a document.
func writeCellValue(buf *bytes.Buffer, v Value) error {
	switch v := v.(type) {
	case *StringVal:
		buf.WriteString(strconv.Quote(v.Value))
	case *IntVal:
		buf.WriteString(v.Raw)
	case *FloatVal:
		buf.WriteString(v.Raw)
	case *BoolVal:
		if v.Value {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case *BytesVal:
		buf.WriteString(`b"`)
		buf.WriteString(base64.StdEncoding.EncodeToString(v.Value))
		buf.WriteByte('"')
	case *NullVal:
		buf.WriteString("null")
	case *IdentVal:
		buf.WriteString(v.Name)
	case *TimestampVal:
		buf.WriteString(v.Raw)
	case *DurationVal:
		buf.WriteString(v.Raw)
	case *ListVal, *BlockVal:
		buf.Write(FormatValue(v))
	default:
		return fmt.Errorf("pxf: BindRow: unexpected cell value type %T", v)
	}
	return nil
}
