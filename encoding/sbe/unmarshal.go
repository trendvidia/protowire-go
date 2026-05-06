// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"math"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// fastSet writes v into msg's fd. When msg's underlying implementation
// exposes SetUnsafe (the trendvidia/protobuf-go fork's addition on
// *dynamicpb.Message) we can skip the runtime typecheck because the
// codec template was built from the same descriptor and already knows
// fd's [protoreflect.Kind]. That avoids the v.Interface() boxing alloc
// (~16 B per scalar set on values outside Go's small-int pool) —
// roughly half the per-Unmarshal allocations on dynamicpb-backed
// messages.
//
// The fast path is opt-in: when consumers depend on upstream
// google.golang.org/protobuf (no SetUnsafe method), the type
// assertion fails and we fall through to [protoreflect.Message.Set].
// This keeps protowire-go compiling against either backend.
func fastSet(msg protoreflect.Message, fd protoreflect.FieldDescriptor, v protoreflect.Value) {
	if u, ok := msg.(interface {
		SetUnsafe(protoreflect.FieldDescriptor, protoreflect.Value)
	}); ok {
		u.SetUnsafe(fd, v)
		return
	}
	msg.Set(fd, v)
}

// fastAppend mirrors [fastSet] for repeating-group elements: skips the
// per-Append typecheck when the list is a dynamicpb list whose element
// kind we already established from the template.
func fastAppend(list protoreflect.List, v protoreflect.Value) {
	if u, ok := list.(interface {
		AppendUnsafe(protoreflect.Value)
	}); ok {
		u.AppendUnsafe(v)
		return
	}
	list.Append(v)
}

func unmarshalMessage(data []byte, msg protoreflect.Message, tmpl *messageTemplate) error {
	if len(data) < headerSize {
		return fmt.Errorf("sbe: data too short for header: %d bytes", len(data))
	}

	blockLength := byteOrder.Uint16(data[0:])
	templateID := byteOrder.Uint16(data[2:])

	if templateID != tmpl.templateID {
		return fmt.Errorf("sbe: template ID mismatch: got %d, want %d", templateID, tmpl.templateID)
	}

	// Schema evolution allows the wire's blockLength to be >= the template's
	// (newer schemas append fields at the end of the block). A wire blockLength
	// smaller than the template's would underrun field reads — reject before
	// slicing.
	if blockLength < tmpl.blockLength {
		return fmt.Errorf("sbe: wire blockLength %d < schema blockLength %d for template %d",
			blockLength, tmpl.blockLength, tmpl.templateID)
	}

	end := headerSize + int(blockLength)
	if len(data) < end {
		return fmt.Errorf("sbe: data too short for root block: need %d, have %d", end, len(data))
	}

	block := data[headerSize:end]
	for _, ft := range tmpl.fields {
		readField(block, ft, msg)
	}

	// Read repeating groups.
	pos := end
	for _, gt := range tmpl.groups {
		n, err := unmarshalGroup(data[pos:], msg, gt)
		if err != nil {
			return err
		}
		pos += n
	}

	return nil
}

func readField(block []byte, ft fieldTemplate, msg protoreflect.Message) {
	if len(ft.composite) > 0 {
		subMsg := msg.Mutable(ft.fd).Message()
		sub := block[ft.offset : int(ft.offset)+int(ft.size)]
		for _, sf := range ft.composite {
			readField(sub, sf, subMsg)
		}
		return
	}

	off := int(ft.offset)

	switch ft.encoding {
	case encInt8:
		setIntField(msg, ft.fd, int64(int8(block[off])))
	case encInt16:
		setIntField(msg, ft.fd, int64(int16(byteOrder.Uint16(block[off:]))))
	case encInt32:
		setIntField(msg, ft.fd, int64(int32(byteOrder.Uint32(block[off:]))))
	case encInt64:
		setIntField(msg, ft.fd, int64(byteOrder.Uint64(block[off:])))
	case encUint8:
		setUintField(msg, ft.fd, uint64(block[off]))
	case encUint16:
		setUintField(msg, ft.fd, uint64(byteOrder.Uint16(block[off:])))
	case encUint32:
		setUintField(msg, ft.fd, uint64(byteOrder.Uint32(block[off:])))
	case encUint64:
		setUintField(msg, ft.fd, byteOrder.Uint64(block[off:]))
	case encFloat:
		setFloatField(msg, ft.fd, float64(math.Float32frombits(byteOrder.Uint32(block[off:]))))
	case encDouble:
		setFloatField(msg, ft.fd, math.Float64frombits(byteOrder.Uint64(block[off:])))
	case encChar:
		end := off + int(ft.size)
		raw := block[off:end]
		switch ft.fd.Kind() {
		case protoreflect.BytesKind:
			b := make([]byte, ft.size)
			copy(b, raw)
			fastSet(msg, ft.fd, protoreflect.ValueOfBytes(b))
		default: // StringKind — trim trailing null padding.
			n := len(raw)
			for n > 0 && raw[n-1] == 0 {
				n--
			}
			fastSet(msg, ft.fd, protoreflect.ValueOfString(string(raw[:n])))
		}
	}
}

func unmarshalGroup(data []byte, msg protoreflect.Message, gt groupTemplate) (int, error) {
	if len(data) < groupHeaderSize {
		return 0, fmt.Errorf("sbe: data too short for group header")
	}

	blockLength := int(byteOrder.Uint16(data[0:]))
	numInGroup := int(byteOrder.Uint16(data[2:]))

	// Reject undersized per-entry block: a wire blockLength below the
	// template's would cause field reads to slice past the entry. Schema
	// evolution may push it higher, which is fine.
	if blockLength < int(gt.blockLength) {
		return 0, fmt.Errorf("sbe: group %s wire blockLength %d < schema blockLength %d",
			gt.fd.Name(), blockLength, gt.blockLength)
	}

	// Bound numInGroup against the available data without computing the
	// product directly: numInGroup*blockLength can overflow int on 32-bit
	// platforms (each factor is uint16, product can reach ~4.3 G).
	remaining := len(data) - groupHeaderSize
	if blockLength > 0 && numInGroup > remaining/blockLength {
		return 0, fmt.Errorf("sbe: group %s declares %d entries × %d bytes, %d bytes remaining",
			gt.fd.Name(), numInGroup, blockLength, remaining)
	}
	totalSize := groupHeaderSize + numInGroup*blockLength

	list := msg.Mutable(gt.fd).List()
	for i := 0; i < numInGroup; i++ {
		entryVal := list.NewElement()
		entryMsg := entryVal.Message()
		start := groupHeaderSize + i*blockLength
		entry := data[start : start+blockLength]
		for _, ft := range gt.fields {
			readField(entry, ft, entryMsg)
		}
		fastAppend(list, entryVal)
	}

	return totalSize, nil
}

// setIntField dispatches to the correct ValueOfIntN constructor.
func setIntField(msg protoreflect.Message, fd protoreflect.FieldDescriptor, v int64) {
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		fastSet(msg, fd, protoreflect.ValueOfInt32(int32(v)))
	default:
		fastSet(msg, fd, protoreflect.ValueOfInt64(v))
	}
}

// setUintField dispatches to the correct ValueOfUintN / bool / enum constructor.
func setUintField(msg protoreflect.Message, fd protoreflect.FieldDescriptor, v uint64) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		fastSet(msg, fd, protoreflect.ValueOfBool(v != 0))
	case protoreflect.EnumKind:
		fastSet(msg, fd, protoreflect.ValueOfEnum(protoreflect.EnumNumber(v)))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		fastSet(msg, fd, protoreflect.ValueOfUint32(uint32(v)))
	default:
		fastSet(msg, fd, protoreflect.ValueOfUint64(v))
	}
}

// setFloatField dispatches to ValueOfFloat32 or ValueOfFloat64.
func setFloatField(msg protoreflect.Message, fd protoreflect.FieldDescriptor, v float64) {
	switch fd.Kind() {
	case protoreflect.FloatKind:
		fastSet(msg, fd, protoreflect.ValueOfFloat32(float32(v)))
	default:
		fastSet(msg, fd, protoreflect.ValueOfFloat64(v))
	}
}
