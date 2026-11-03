// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func marshalMessage(msg protoreflect.Message, tmpl *messageTemplate) ([]byte, error) {
	// Pre-calculate total size including groups.
	totalSize := headerSize + int(tmpl.blockLength)
	for _, gt := range tmpl.groups {
		n := msg.Get(gt.fd).List().Len()
		// numInGroup is a uint16: a larger count would be written as its low
		// 16 bits, and a reader would take the entries past them for trailing
		// bytes (issue #250).
		if n > maxUint16 {
			return nil, fmt.Errorf("sbe: field %s has %d entries, past %d, the largest numInGroup SBE can state", gt.fd.FullName(), n, maxUint16)
		}
		totalSize += groupHeaderSize + n*int(gt.blockLength)
	}

	buf := make([]byte, totalSize)

	// Write message header.
	byteOrder.PutUint16(buf[0:], tmpl.blockLength)
	byteOrder.PutUint16(buf[2:], tmpl.templateID)
	byteOrder.PutUint16(buf[4:], tmpl.schemaID)
	byteOrder.PutUint16(buf[6:], tmpl.version)

	// Write root block fields.
	block := buf[headerSize : headerSize+int(tmpl.blockLength)]
	for i := range tmpl.fields {
		if err := writeField(block, &tmpl.fields[i], msg); err != nil {
			return nil, err
		}
	}

	// Write repeating groups.
	pos := headerSize + int(tmpl.blockLength)
	for i := range tmpl.groups {
		n, err := marshalGroup(buf[pos:], msg, &tmpl.groups[i])
		if err != nil {
			return nil, err
		}
		pos += n
	}

	return buf, nil
}

func writeField(block []byte, ft *fieldTemplate, msg protoreflect.Message) error {
	if len(ft.composite) > 0 {
		subMsg := msg.Get(ft.fd).Message()
		sub := block[ft.offset : int(ft.offset)+int(ft.size)]
		for i := range ft.composite {
			if err := writeField(sub, &ft.composite[i], subMsg); err != nil {
				return err
			}
		}
		return nil
	}

	val := msg.Get(ft.fd)
	off := int(ft.offset)

	switch ft.encoding {
	case encFloat:
		f := val.Float()
		if ft.writeCheck && !floatFits(f) {
			return fmt.Errorf("sbe: field %s: value %g is outside the range of float, ±%g", ft.fd.FullName(), f, math.MaxFloat32)
		}
		byteOrder.PutUint32(block[off:], math.Float32bits(float32(f)))
	case encDouble:
		byteOrder.PutUint64(block[off:], math.Float64bits(val.Float()))
	case encChar:
		end := off + int(ft.size)
		switch ft.fd.Kind() {
		case protoreflect.BytesKind:
			copy(block[off:end], val.Bytes())
		default:
			copy(block[off:end], cutString(val.String(), int(ft.size)))
		}
	default:
		// An integer primitive. The value is read by the field's kind, and
		// written as the low bytes of its two's complement once it is known to
		// fit (issue #252).
		var v uint64
		unsigned := false
		switch ft.kind {
		case protoreflect.BoolKind:
			if val.Bool() {
				v = 1
			}
			unsigned = true
		case protoreflect.EnumKind:
			v = uint64(int64(val.Enum()))
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			v = val.Uint()
			unsigned = true
		default:
			v = uint64(val.Int())
		}
		if ft.writeCheck && !inRange(v, unsigned, ft.encLo, ft.encHi) {
			return fmt.Errorf("sbe: field %s: value %s is outside the range of %s, [%d, %d]", ft.fd.FullName(), intString(v, unsigned), ft.encoding, ft.encLo, ft.encHi)
		}
		switch ft.size {
		case 1:
			block[off] = byte(v)
		case 2:
			byteOrder.PutUint16(block[off:], uint16(v))
		case 4:
			byteOrder.PutUint32(block[off:], uint32(v))
		default:
			byteOrder.PutUint64(block[off:], v)
		}
	}
	return nil
}

// intString formats an integer held as in inRange.
func intString(v uint64, unsigned bool) string {
	if unsigned {
		return strconv.FormatUint(v, 10)
	}
	return strconv.FormatInt(int64(v), 10)
}

// cutString shortens s to at most n bytes without splitting a UTF-8
// character (draft -01 § SBE Annotations): a byte-exact cut can leave half a
// character, and a strict decoder then rejects the whole message. The bytes
// the cut leaves over stay NUL, as the block is zeroed. A bytes field is cut
// at the limit instead.
func cutString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func marshalGroup(buf []byte, msg protoreflect.Message, gt *groupTemplate) (int, error) {
	list := msg.Get(gt.fd).List()
	n := list.Len()

	byteOrder.PutUint16(buf[0:], gt.blockLength)
	byteOrder.PutUint16(buf[2:], uint16(n))

	for i := 0; i < n; i++ {
		entryMsg := list.Get(i).Message()
		start := groupHeaderSize + i*int(gt.blockLength)
		entry := buf[start : start+int(gt.blockLength)]
		for j := range gt.fields {
			if err := writeField(entry, &gt.fields[j], entryMsg); err != nil {
				return 0, err
			}
		}
	}

	return groupHeaderSize + n*int(gt.blockLength), nil
}
