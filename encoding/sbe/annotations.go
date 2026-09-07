// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Extension field numbers from sbe/annotations.proto.
const (
	extSchemaID   protoreflect.FieldNumber = 1319
	extVersion    protoreflect.FieldNumber = 1320
	extTemplateID protoreflect.FieldNumber = 1321
	extLength     protoreflect.FieldNumber = 1322
	extEncoding   protoreflect.FieldNumber = 1323
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

// Retired option numbers (STABILITY.md promise 3, #98). Before protowire
// v1.12.0 the SBE options lived at 50100–50301; a descriptor compiled
// then still carries them, and this codec — which reads only the
// registered numbers — used to report such a file as "missing
// (sbe.schema_id)", sending the reader to check a .proto that does
// declare it. The two numbers whose absence the codec reports are
// diagnosed instead. encoding/pxf's bind-time check covers the whole
// retired surface with the reasoning behind the scoping; here the file
// or message was handed to the SBE codec explicitly, so no gate is
// needed.
const (
	retiredSchemaID   protoreflect.FieldNumber = 50100
	retiredTemplateID protoreflect.FieldNumber = 50200
)

// hasUnknownField reports whether rm's unknown bytes carry field num.
// Nothing declares the retired numbers any more, so unknown bytes are
// where they land.
func hasUnknownField(rm protoreflect.Message, num protoreflect.FieldNumber) bool {
	if !rm.IsValid() {
		return false
	}
	b := rm.GetUnknown()
	for len(b) > 0 {
		fnum, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		vn := protowire.ConsumeFieldValue(fnum, wtype, b[n:])
		if vn < 0 {
			return false
		}
		if fnum == num {
			return true
		}
		b = b[n+vn:]
	}
	return false
}

// staleFileError is the diagnosis for a file whose (sbe.schema_id) sits
// at the retired number, or nil when it does not.
func staleFileError(fd protoreflect.FileDescriptor) error {
	opts, ok := fd.Options().(*descriptorpb.FileOptions)
	if !ok || opts == nil || !hasUnknownField(opts.ProtoReflect(), retiredSchemaID) {
		return nil
	}
	return fmt.Errorf("sbe: file %s carries (sbe.schema_id) at retired option number %d (registered as %d since protowire v1.12.0): "+
		"the descriptor predates the registered extension block and must be recompiled against the current sbe/annotations.proto",
		fd.Path(), retiredSchemaID, extSchemaID)
}

// staleMessageError is the same diagnosis for a message whose
// (sbe.template_id) sits at the retired number.
func staleMessageError(md protoreflect.MessageDescriptor) error {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || opts == nil || !hasUnknownField(opts.ProtoReflect(), retiredTemplateID) {
		return nil
	}
	return fmt.Errorf("sbe: message %s carries (sbe.template_id) at retired option number %d (registered as %d since protowire v1.12.0): "+
		"the descriptor predates the registered extension block and must be recompiled against the current sbe/annotations.proto",
		md.FullName(), retiredTemplateID, extTemplateID)
}
