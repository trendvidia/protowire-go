// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Extension field numbers from pxf/annotations.proto.
const (
	extRequired protoreflect.FieldNumber = 50000
	extDefault  protoreflect.FieldNumber = 50001
)

// IsRequired reports whether the field has (pxf.required) = true.
// Exported for layered-config consumers (e.g. chameleon) that run
// their own post-merge required-validation pass with SkipPostDecode.
func IsRequired(fd protoreflect.FieldDescriptor) bool {
	return getBoolOption(fd, extRequired)
}

// Default returns the (pxf.default) value string if set. The string is
// a PXF literal (e.g. `42`, `true`, `"hello"`); callers parse it with
// [ApplyDefault] or their own logic. Exported for layered-config
// consumers running post-merge defaults passes.
func Default(fd protoreflect.FieldDescriptor) (string, bool) {
	return getStringOption(fd, extDefault)
}

// isRequired and getDefault are kept as private aliases so the
// existing in-package callsites (postDecode) don't churn.
func isRequired(fd protoreflect.FieldDescriptor) bool { return IsRequired(fd) }
func getDefault(fd protoreflect.FieldDescriptor) (string, bool) {
	return Default(fd)
}

// findNullMaskField returns the "_null" field if it exists and is a
// google.protobuf.FieldMask. Both the name and type must match.
func findNullMaskField(desc protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	fd := desc.Fields().ByName("_null")
	if fd == nil {
		return nil
	}
	if fd.Kind() == protoreflect.MessageKind &&
		fd.Message().FullName() == "google.protobuf.FieldMask" {
		return fd
	}
	return nil
}

// getBoolOption reads a bool extension from field options.
// It checks known fields first (protocompile resolves extensions as known fields),
// then falls back to parsing raw unknown bytes (for protoc-produced descriptors).
func getBoolOption(fd protoreflect.FieldDescriptor, num protoreflect.FieldNumber) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return false
	}
	rm := opts.ProtoReflect()

	// Check known fields (protocompile stores resolved extensions here).
	var found bool
	rm.Range(func(ofd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if ofd.Number() == num {
			found = v.Bool()
			return false
		}
		return true
	})
	if found {
		return true
	}

	// Fallback: parse raw unknown bytes (protoc / descriptor-based).
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
				return false
			}
			if fnum == num {
				return v != 0
			}
			b = b[vn:]
		case protowire.Fixed32Type:
			b = b[4:]
		case protowire.Fixed64Type:
			b = b[8:]
		case protowire.BytesType:
			_, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return false
			}
			b = b[vn:]
		default:
			return false
		}
	}
	return false
}

// getStringOption reads a string extension from field options.
// Checks known fields first, then falls back to raw unknown bytes.
func getStringOption(fd protoreflect.FieldDescriptor, num protoreflect.FieldNumber) (string, bool) {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return "", false
	}
	rm := opts.ProtoReflect()

	// Check known fields (protocompile stores resolved extensions here).
	var result string
	var found bool
	rm.Range(func(ofd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if ofd.Number() == num {
			result = v.String()
			found = true
			return false
		}
		return true
	})
	if found {
		return result, true
	}

	// Fallback: parse raw unknown bytes.
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
