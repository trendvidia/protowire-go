// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"math"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func marshalMessage(msg protoreflect.Message, tmpl *messageTemplate) ([]byte, error) {
	// Pre-calculate total size including groups.
	totalSize := headerSize + int(tmpl.blockLength)
	for _, gt := range tmpl.groups {
		list := msg.Get(gt.fd).List()
		totalSize += groupHeaderSize + list.Len()*int(gt.blockLength)
	}

	buf := make([]byte, totalSize)

	// Write message header.
	byteOrder.PutUint16(buf[0:], tmpl.blockLength)
	byteOrder.PutUint16(buf[2:], tmpl.templateID)
	byteOrder.PutUint16(buf[4:], tmpl.schemaID)
	byteOrder.PutUint16(buf[6:], tmpl.version)

	// Write root block fields.
	block := buf[headerSize : headerSize+int(tmpl.blockLength)]
	for _, ft := range tmpl.fields {
		writeField(block, ft, msg)
	}

	// Write repeating groups.
	pos := headerSize + int(tmpl.blockLength)
	for _, gt := range tmpl.groups {
		n := marshalGroup(buf[pos:], msg, gt)
		pos += n
	}

	return buf, nil
}

func writeField(block []byte, ft fieldTemplate, msg protoreflect.Message) {
	if len(ft.composite) > 0 {
		subMsg := msg.Get(ft.fd).Message()
		sub := block[ft.offset : int(ft.offset)+int(ft.size)]
		for _, sf := range ft.composite {
			writeField(sub, sf, subMsg)
		}
		return
	}

	val := msg.Get(ft.fd)
	off := int(ft.offset)

	switch ft.encoding {
	case encInt8:
		block[off] = byte(int8(val.Int()))
	case encInt16:
		byteOrder.PutUint16(block[off:], uint16(int16(val.Int())))
	case encInt32:
		byteOrder.PutUint32(block[off:], uint32(int32(val.Int())))
	case encInt64:
		byteOrder.PutUint64(block[off:], uint64(val.Int()))
	case encUint8:
		block[off] = byte(uintVal(ft.fd, val))
	case encUint16:
		byteOrder.PutUint16(block[off:], uint16(uintVal(ft.fd, val)))
	case encUint32:
		byteOrder.PutUint32(block[off:], uint32(uintVal(ft.fd, val)))
	case encUint64:
		byteOrder.PutUint64(block[off:], uintVal(ft.fd, val))
	case encFloat:
		byteOrder.PutUint32(block[off:], math.Float32bits(float32(val.Float())))
	case encDouble:
		byteOrder.PutUint64(block[off:], math.Float64bits(val.Float()))
	case encChar:
		end := off + int(ft.size)
		switch ft.fd.Kind() {
		case protoreflect.BytesKind:
			copy(block[off:end], val.Bytes())
		default:
			copy(block[off:end], val.String())
		}
	}
}

func marshalGroup(buf []byte, msg protoreflect.Message, gt groupTemplate) int {
	list := msg.Get(gt.fd).List()
	n := list.Len()

	byteOrder.PutUint16(buf[0:], gt.blockLength)
	byteOrder.PutUint16(buf[2:], uint16(n))

	for i := 0; i < n; i++ {
		entryMsg := list.Get(i).Message()
		start := groupHeaderSize + i*int(gt.blockLength)
		entry := buf[start : start+int(gt.blockLength)]
		for _, ft := range gt.fields {
			writeField(entry, ft, entryMsg)
		}
	}

	return groupHeaderSize + n*int(gt.blockLength)
}

// uintVal reads an unsigned integer value from a proto field, handling
// bool and enum kinds which use different Value accessors.
func uintVal(fd protoreflect.FieldDescriptor, val protoreflect.Value) uint64 {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		if val.Bool() {
			return 1
		}
		return 0
	case protoreflect.EnumKind:
		return uint64(val.Enum())
	default:
		return val.Uint()
	}
}
