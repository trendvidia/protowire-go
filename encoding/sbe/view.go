// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"math"
	"unsafe"
)

// viewSchema holds pre-computed name-to-field lookup for zero-allocation View access.
type viewSchema struct {
	fields     map[string]*fieldTemplate
	groupOrder []viewGroupInfo
}

type viewGroupInfo struct {
	name   string
	schema *viewSchema // field lookup for group entries
}

func buildViewSchema(tmpl *messageTemplate) *viewSchema {
	vs := &viewSchema{
		fields: make(map[string]*fieldTemplate, len(tmpl.fields)),
	}
	for i := range tmpl.fields {
		ft := &tmpl.fields[i]
		vs.fields[string(ft.fd.Name())] = ft
		if len(ft.composite) > 0 {
			ft.compositeView = buildFieldsViewSchema(ft.composite)
		}
	}
	for i := range tmpl.groups {
		gt := &tmpl.groups[i]
		vs.groupOrder = append(vs.groupOrder, viewGroupInfo{
			name:   string(gt.fd.Name()),
			schema: buildFieldsViewSchema(gt.fields),
		})
	}
	return vs
}

func buildFieldsViewSchema(fields []fieldTemplate) *viewSchema {
	vs := &viewSchema{
		fields: make(map[string]*fieldTemplate, len(fields)),
	}
	for i := range fields {
		ft := &fields[i]
		vs.fields[string(ft.fd.Name())] = ft
		if len(ft.composite) > 0 {
			ft.compositeView = buildFieldsViewSchema(ft.composite)
		}
	}
	return vs
}

// View provides zero-allocation read access to SBE-encoded data.
// Field values are read directly from the underlying byte buffer.
//
// Strings returned by [View.String] are backed by the original buffer
// (via [unsafe.String]) and are only valid while that buffer is alive.
//
// # Trust model
//
// View is designed for the hot path and trades safety for speed. Once
// the constructing [Codec.View] call returns successfully, accessor
// methods ([View.Int], [View.Uint], [View.String], [View.Composite],
// [View.Group], etc.) panic on schema mismatch — unknown field name,
// wrong primitive type, or a field accessed as the wrong category. They
// are intended for callers that own both the schema and the buffer.
//
// Do not call View accessors with arbitrary, attacker-controlled bytes.
// For untrusted input, use [Codec.Unmarshal] or [Codec.UnmarshalDescriptor],
// which return an error on every malformed-input path. View's constructor
// validates the message header and rejects buffers shorter than the
// template's declared block length, but accessor-level checks are
// intentionally skipped.
type View struct {
	data   []byte      // full SBE message (for group traversal)
	block  []byte      // current block (root, entry, or composite)
	schema *viewSchema // field lookup
}

// GroupView provides access to entries in an SBE repeating group.
type GroupView struct {
	data        []byte      // group data starting at group header
	blockLength int         // per-entry block size
	count       int         // number of entries
	schema      *viewSchema // entry field lookup
}

// View creates a zero-allocation reader over SBE-encoded data.
// The message template is identified from the header's template ID.
func (c *Codec) View(data []byte) (View, error) {
	if len(data) < headerSize {
		return View{}, fmt.Errorf("sbe: data too short for header")
	}
	blockLength := int(byteOrder.Uint16(data[0:]))
	templateID := byteOrder.Uint16(data[2:])
	tmpl, ok := c.byID[templateID]
	if !ok {
		return View{}, fmt.Errorf("sbe: unknown template ID %d", templateID)
	}
	if blockLength < int(tmpl.blockLength) {
		return View{}, fmt.Errorf("sbe: wire blockLength %d < schema blockLength %d for template %d",
			blockLength, tmpl.blockLength, templateID)
	}
	end := headerSize + blockLength
	if len(data) < end {
		return View{}, fmt.Errorf("sbe: data too short for root block")
	}
	return View{
		data:   data,
		block:  data[headerSize:end],
		schema: tmpl.view,
	}, nil
}

func (v View) field(name string) *fieldTemplate {
	ft := v.schema.fields[name]
	if ft == nil {
		panic("sbe: unknown field: " + name)
	}
	return ft
}

// Int reads a signed integer field. Works with int8, int16, int32, int64 encodings.
func (v View) Int(name string) int64 {
	ft := v.field(name)
	off := int(ft.offset)
	switch ft.encoding {
	case encInt8:
		return int64(int8(v.block[off]))
	case encInt16:
		return int64(int16(byteOrder.Uint16(v.block[off:])))
	case encInt32:
		return int64(int32(byteOrder.Uint32(v.block[off:])))
	case encInt64:
		return int64(byteOrder.Uint64(v.block[off:]))
	default:
		panic("sbe: field " + name + " is not a signed integer")
	}
}

// Uint reads an unsigned integer field. Works with uint8, uint16, uint32, uint64 encodings.
// Also works for bool and enum fields (returns raw numeric value).
func (v View) Uint(name string) uint64 {
	ft := v.field(name)
	off := int(ft.offset)
	switch ft.encoding {
	case encUint8:
		return uint64(v.block[off])
	case encUint16:
		return uint64(byteOrder.Uint16(v.block[off:]))
	case encUint32:
		return uint64(byteOrder.Uint32(v.block[off:]))
	case encUint64:
		return byteOrder.Uint64(v.block[off:])
	default:
		panic("sbe: field " + name + " is not an unsigned integer")
	}
}

// Float reads a floating-point field. Works with float and double encodings.
func (v View) Float(name string) float64 {
	ft := v.field(name)
	off := int(ft.offset)
	switch ft.encoding {
	case encFloat:
		return float64(math.Float32frombits(byteOrder.Uint32(v.block[off:])))
	case encDouble:
		return math.Float64frombits(byteOrder.Uint64(v.block[off:]))
	default:
		panic("sbe: field " + name + " is not a float")
	}
}

// Bool reads a boolean field (uint8 encoding: 0 = false).
func (v View) Bool(name string) bool {
	ft := v.field(name)
	return v.block[ft.offset] != 0
}

// Enum reads an enum field as an integer.
func (v View) Enum(name string) int {
	ft := v.field(name)
	off := int(ft.offset)
	switch ft.encoding {
	case encUint8:
		return int(v.block[off])
	case encUint16:
		return int(byteOrder.Uint16(v.block[off:]))
	default:
		panic("sbe: field " + name + " has unsupported enum encoding")
	}
}

// String reads a fixed-length string field. The returned string points directly
// into the underlying buffer (zero-copy) and is only valid while that buffer is alive.
// Trailing null padding is trimmed.
func (v View) String(name string) string {
	ft := v.field(name)
	off := int(ft.offset)
	raw := v.block[off : off+int(ft.size)]
	n := len(raw)
	for n > 0 && raw[n-1] == 0 {
		n--
	}
	if n == 0 {
		return ""
	}
	return unsafe.String(&raw[0], n)
}

// Bytes reads a fixed-length bytes field as a sub-slice of the underlying buffer.
func (v View) Bytes(name string) []byte {
	ft := v.field(name)
	off := int(ft.offset)
	return v.block[off : off+int(ft.size)]
}

// Composite returns a sub-View over a nested message (SBE composite) field.
func (v View) Composite(name string) View {
	ft := v.field(name)
	if ft.compositeView == nil {
		panic("sbe: field " + name + " is not a composite")
	}
	return View{
		block:  v.block[ft.offset : int(ft.offset)+int(ft.size)],
		schema: ft.compositeView,
	}
}

// Group returns a GroupView for a repeating group field.
// Groups are located by walking the data sequentially from the end of the root block.
func (v View) Group(name string) GroupView {
	pos := headerSize + len(v.block)
	for _, gi := range v.schema.groupOrder {
		bl := int(byteOrder.Uint16(v.data[pos:]))
		n := int(byteOrder.Uint16(v.data[pos+2:]))
		if gi.name == name {
			return GroupView{
				data:        v.data[pos:],
				blockLength: bl,
				count:       n,
				schema:      gi.schema,
			}
		}
		pos += groupHeaderSize + n*bl
	}
	panic("sbe: unknown group: " + name)
}

// Len returns the number of entries in the group.
func (g GroupView) Len() int { return g.count }

// Entry returns a View over the i-th group entry.
func (g GroupView) Entry(i int) View {
	start := groupHeaderSize + i*g.blockLength
	return View{
		block:  g.data[start : start+g.blockLength],
		schema: g.schema,
	}
}
