// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"math"
	"unicode/utf8"

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

func unmarshalMessage(data []byte, msg protoreflect.Message, tmpl *messageTemplate, maxRepeated int) error {
	if len(data) < headerSize {
		return fmt.Errorf("sbe: data too short for header: %d bytes", len(data))
	}

	blockLength := byteOrder.Uint16(data[0:])
	templateID := byteOrder.Uint16(data[2:])
	schemaID := byteOrder.Uint16(data[4:])

	if templateID != tmpl.templateID {
		return fmt.Errorf("sbe: template ID mismatch: got %d, want %d", templateID, tmpl.templateID)
	}
	// A template id is unique only within a schema (issue #253), so the
	// template id alone does not say the bytes are this message's: they
	// may be another schema's message with the same id (issue #257).
	if schemaID != tmpl.schemaID {
		return fmt.Errorf("sbe: schema ID mismatch: got %d, want %d for template %d", schemaID, tmpl.schemaID, tmpl.templateID)
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
	for i := range tmpl.fields {
		if err := readField(block, &tmpl.fields[i], msg); err != nil {
			return err
		}
	}

	// Read repeating groups.
	pos := end
	for _, gt := range tmpl.groups {
		n, err := unmarshalGroup(data[pos:], msg, gt, maxRepeated)
		if err != nil {
			return err
		}
		pos += n
	}

	return nil
}

func readField(block []byte, ft *fieldTemplate, msg protoreflect.Message) error {
	if len(ft.composite) > 0 {
		subMsg := msg.Mutable(ft.fd).Message()
		sub := block[ft.offset : int(ft.offset)+int(ft.size)]
		for i := range ft.composite {
			if err := readField(sub, &ft.composite[i], subMsg); err != nil {
				return err
			}
		}
		return nil
	}

	off := int(ft.offset)

	switch ft.encoding {
	case encFloat:
		setFloatField(msg, ft.fd, float64(math.Float32frombits(byteOrder.Uint32(block[off:]))))
	case encDouble:
		f := math.Float64frombits(byteOrder.Uint64(block[off:]))
		if ft.readCheck && !floatFits(f) {
			return fmt.Errorf("sbe: field %s: wire value %g does not fit a float field, ±%g", ft.fd.FullName(), f, math.MaxFloat32)
		}
		setFloatField(msg, ft.fd, f)
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
			// HARDENING.md § UTF-8: a string field is valid UTF-8 whatever the
			// source encoding, and a char[] says nothing about its own. Bytes
			// that are text in another encoding belong in a bytes field.
			if !utf8.Valid(raw[:n]) {
				return fmt.Errorf("sbe: invalid UTF-8 in string field %s", ft.fd.Name())
			}
			fastSet(msg, ft.fd, protoreflect.ValueOfString(string(raw[:n])))
		}
	default:
		// An integer primitive, sign- or zero-extended per the primitive
		// (draft -01 § SBE Annotations), then stored by the field's kind. A
		// value the field cannot hold is refused rather than cut to its low
		// bits (issue #252).
		var v uint64
		switch ft.size {
		case 1:
			v = uint64(block[off])
		case 2:
			v = uint64(byteOrder.Uint16(block[off:]))
		case 4:
			v = uint64(byteOrder.Uint32(block[off:]))
		default:
			v = byteOrder.Uint64(block[off:])
		}
		if ft.encSigned {
			shift := 64 - 8*uint(ft.size)
			v = uint64(int64(v<<shift) >> shift)
		}
		if ft.readCheck && !inRange(v, !ft.encSigned, ft.fieldLo, ft.fieldHi) {
			return fmt.Errorf("sbe: field %s: wire value %s (%s) does not fit a %s field, [%d, %d]", ft.fd.FullName(), intString(v, !ft.encSigned), ft.encoding, ft.fd.Kind(), ft.fieldLo, ft.fieldHi)
		}
		setIntegerField(msg, ft.fd, ft.kind, v)
	}
	return nil
}

func unmarshalGroup(data []byte, msg protoreflect.Message, gt groupTemplate, maxRepeated int) (int, error) {
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
	if numInGroup > maxRepeated {
		return 0, fmt.Errorf("sbe: group %s declares %d entries, MaxRepeatedCount=%d", gt.fd.Name(), numInGroup, maxRepeated)
	}
	totalSize := groupHeaderSize + numInGroup*blockLength

	list := msg.Mutable(gt.fd).List()
	for i := 0; i < numInGroup; i++ {
		entryVal := list.NewElement()
		entryMsg := entryVal.Message()
		start := groupHeaderSize + i*blockLength
		entry := data[start : start+blockLength]
		for j := range gt.fields {
			if err := readField(entry, &gt.fields[j], entryMsg); err != nil {
				return 0, err
			}
		}
		fastAppend(list, entryVal)
	}

	return totalSize, nil
}

// setIntegerField stores an integer that fits fd as the Value fd's kind takes.
// v holds an int64's bits or a uint64, whichever the primitive was; once it
// fits, both read the same.
func setIntegerField(msg protoreflect.Message, fd protoreflect.FieldDescriptor, kind protoreflect.Kind, v uint64) {
	switch kind {
	case protoreflect.BoolKind:
		fastSet(msg, fd, protoreflect.ValueOfBool(v != 0))
	case protoreflect.EnumKind:
		fastSet(msg, fd, protoreflect.ValueOfEnum(protoreflect.EnumNumber(int32(v))))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		fastSet(msg, fd, protoreflect.ValueOfInt32(int32(v)))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		fastSet(msg, fd, protoreflect.ValueOfInt64(int64(v)))
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
