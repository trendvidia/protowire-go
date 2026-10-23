// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Per-row proto binding for @dataset rows. Sits atop the streaming
// DatasetReader (DatasetReader.Scan) and is also exported as a standalone
// helper (BindRow) for callers that iterate the materializing path's
// Result.Datasets()[i].Rows.
//
// Implementation strategy: convert each non-nil cell back to its PXF
// text representation, concatenate as a `<column> = <value>\n` body,
// and run through the existing decoder: Unmarshal, or UnmarshalFull for
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
	return BindRow(msg, tr.columns, row)
}

// BindRow binds row.Cells to the fields of msg by column name. The
// columns slice MUST have the same length as row.Cells; a mismatch is
// a programmer error and returns an error.
//
// Exported so callers iterating the materializing path's
// Result.Datasets()[i].Rows can reuse the same logic. Same cell-state
// semantics as [DatasetReader.Scan].
func BindRow(msg proto.Message, columns []string, row DatasetRow) error {
	if len(columns) != len(row.Cells) {
		return fmt.Errorf("pxf: BindRow: %d columns vs %d cells", len(columns), len(row.Cells))
	}
	body, err := rowToPXFBody(columns, row)
	if err != nil {
		return err
	}
	// Both paths run the bind-time schema checks, as Unmarshal does by
	// default: they are memoized per descriptor, so a row pays a cache
	// lookup for them, and they are what make the annotations
	// well-defined (two spellings of one default that disagree,
	// (pxf.required) on a oneof member).
	if !rowNeedsPresence(msg.ProtoReflect().Descriptor()) {
		return atRow(UnmarshalOptions{}.Unmarshal(body, msg), row.Pos)
	}
	// The full pipeline's post-decode pass gives an empty cell (no
	// entry) the field's (pxf.default) and rejects it for
	// (pxf.required), and lets a null cell satisfy required and keep the
	// default off (draft §3.4.4); it also records a null cell in the
	// row type's _null mask. It costs a presence record per row, so only
	// a row type that needs it pays for it.
	_, err = UnmarshalOptions{}.UnmarshalFull(body, msg)
	return atRow(err, row.Pos)
}

// atRow moves a decode error to the row it came from. The decoder reads
// a synthetic body of one line per non-empty cell, so its positions are
// lines of that body, and a required field's error has no position at
// all (1:1). A row built by hand has no position to give.
func atRow(err error, pos Position) error {
	pe, ok := err.(*Error)
	if !ok || pos.Line == 0 {
		return err
	}
	return &Error{Pos: pos, Msg: pe.Msg}
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
// declares a _null mask.
func rowNeedsPresence(desc protoreflect.MessageDescriptor) bool {
	cacheable := reflect.TypeOf(desc).Comparable()
	if cacheable {
		if v, hit := rowPresenceCache.Load(desc); hit {
			return v.(bool)
		}
	}
	need := findNullMaskField(desc) != nil || hasDefaultOrRequired(desc)
	if cacheable && rowPresenceCount.Load() < maxCachedDescriptors {
		if _, loaded := rowPresenceCache.LoadOrStore(desc, need); !loaded {
			rowPresenceCount.Add(1)
		}
	}
	return need
}

// hasDefaultOrRequired reports whether a field of desc carries
// (pxf.default) or (pxf.required). Only desc's own fields matter for a
// row: a cell never holds a message that could carry them deeper down
// (v1 cells are literals; the literal-shaped messages declare neither).
func hasDefaultOrRequired(desc protoreflect.MessageDescriptor) bool {
	fields := desc.Fields()
	for i := range fields.Len() {
		if o := pxfFieldOptions(fields.Get(i)); o.defSet || o.required {
			return true
		}
	}
	return false
}

// rowToPXFBody renders a row as a PXF body: one `<column> = <value>`
// entry per non-nil cell, in column order. Empty cells produce no
// entry (the field stays absent from the decoder's perspective).
func rowToPXFBody(columns []string, row DatasetRow) ([]byte, error) {
	var buf bytes.Buffer
	for i, cell := range row.Cells {
		if cell == nil {
			continue
		}
		buf.WriteString(columns[i])
		buf.WriteString(" = ")
		if err := writeCellValue(&buf, cell); err != nil {
			return nil, err
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// writeCellValue formats a single cell value as PXF text. v1 @dataset
// cells are scalar-shaped (no list, no block), so we only handle the
// leaf-value variants — list and block AST nodes are unreachable here
// because parseDatasetRow / consumeRowCell rejects them before the
// streaming reader hands them to BindRow.
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
	default:
		return fmt.Errorf("pxf: BindRow: unexpected cell value type %T (v1 @dataset cells are scalar-shaped)", v)
	}
	return nil
}
