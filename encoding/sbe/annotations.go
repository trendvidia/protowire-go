// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Extension field numbers from sbe/annotations.proto.
const (
	extSchemaID   protoreflect.FieldNumber = 50100
	extVersion    protoreflect.FieldNumber = 50101
	extTemplateID protoreflect.FieldNumber = 50200
	extLength     protoreflect.FieldNumber = 50300
	extEncoding   protoreflect.FieldNumber = 50301
)

func getFileUint32Option(fd protoreflect.FileDescriptor, num protoreflect.FieldNumber) (uint32, bool) {
	opts, ok := fd.Options().(*descriptorpb.FileOptions)
	if !ok || opts == nil {
		return 0, false
	}
	return getUint32FromMessage(opts.ProtoReflect(), num)
}

func getMessageUint32Option(md protoreflect.MessageDescriptor, num protoreflect.FieldNumber) (uint32, bool) {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || opts == nil {
		return 0, false
	}
	return getUint32FromMessage(opts.ProtoReflect(), num)
}

func getFieldUint32Option(fd protoreflect.FieldDescriptor, num protoreflect.FieldNumber) (uint32, bool) {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return 0, false
	}
	return getUint32FromMessage(opts.ProtoReflect(), num)
}

func getFieldStringOption(fd protoreflect.FieldDescriptor, num protoreflect.FieldNumber) (string, bool) {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return "", false
	}
	return getStringFromMessage(opts.ProtoReflect(), num)
}

// getUint32FromMessage reads a uint32 extension by field number.
// Checks known fields first (protocompile), falls back to raw unknown bytes.
func getUint32FromMessage(rm protoreflect.Message, num protoreflect.FieldNumber) (uint32, bool) {
	var result uint32
	var found bool
	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == num {
			result = uint32(v.Uint())
			found = true
			return false
		}
		return true
	})
	if found {
		return result, true
	}

	b := rm.GetUnknown()
	for len(b) > 0 {
		fnum, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		switch wtype {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0, false
			}
			if fnum == num {
				return uint32(v), true
			}
			b = b[vn:]
		case protowire.Fixed32Type:
			v, vn := protowire.ConsumeFixed32(b)
			if vn < 0 {
				return 0, false
			}
			if fnum == num {
				return v, true
			}
			b = b[vn:]
		case protowire.Fixed64Type:
			b = b[8:]
		case protowire.BytesType:
			_, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return 0, false
			}
			b = b[vn:]
		default:
			return 0, false
		}
	}
	return 0, false
}

// getStringFromMessage reads a string extension by field number.
func getStringFromMessage(rm protoreflect.Message, num protoreflect.FieldNumber) (string, bool) {
	var result string
	var found bool
	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == num {
			result = v.String()
			found = true
			return false
		}
		return true
	})
	if found {
		return result, true
	}

	b := rm.GetUnknown()
	for len(b) > 0 {
		fnum, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		switch wtype {
		case protowire.VarintType:
			_, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return "", false
			}
			b = b[vn:]
		case protowire.Fixed32Type:
			b = b[4:]
		case protowire.Fixed64Type:
			b = b[8:]
		case protowire.BytesType:
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return "", false
			}
			if fnum == num {
				return string(v), true
			}
			b = b[vn:]
		default:
			return "", false
		}
	}
	return "", false
}
