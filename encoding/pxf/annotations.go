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
	extRequired protoreflect.FieldNumber = 1314
	extDefault  protoreflect.FieldNumber = 1315
	extKey      protoreflect.FieldNumber = 1316
)

// KeyFieldName returns the raw (pxf.key) annotation value if set — the
// proto field name the schema designates as the key of a keyed repeated
// field (draft -01 §3.13). The value is returned even when its
// placement is invalid; [ValidateFile] reports placement violations and
// [KeyField] resolves the annotation only when it is well-placed.
// Exported for tooling (protolsp, protocheck) that needs the authored
// value for diagnostics.
func KeyFieldName(fd protoreflect.FieldDescriptor) (string, bool) {
	return getStringOption(fd, extKey)
}

// KeyField returns the key field descriptor of a keyed repeated field:
// the singular string field of fd's element message that fd's (pxf.key)
// annotation names (draft -01 §3.13). It returns nil when fd carries no
// (pxf.key) annotation or when the annotation's placement is invalid —
// fd is not a repeated message-typed field, the named field does not
// exist, or it is not a singular string field. Use [ValidateFile] to
// surface invalid placements as violations.
func KeyField(fd protoreflect.FieldDescriptor) protoreflect.FieldDescriptor {
	if fd == nil || !fd.IsList() || fd.Kind() != protoreflect.MessageKind {
		return nil
	}
	name, ok := getStringOption(fd, extKey)
	if !ok || name == "" {
		return nil
	}
	kf := fd.Message().Fields().ByName(protoreflect.Name(name))
	if kf == nil || kf.IsList() || kf.IsMap() || kf.Kind() != protoreflect.StringKind {
		return nil
	}
	return kf
}

// IsKeyed reports whether fd is a keyed repeated field — a repeated
// message-typed field with a valid (pxf.key) annotation. Equivalent to
// KeyField(fd) != nil.
func IsKeyed(fd protoreflect.FieldDescriptor) bool {
	return KeyField(fd) != nil
}

// IsRequired reports whether the field has (pxf.required) = true.
// Exported for layered-config consumers (e.g. chameleon) that run
// their own post-merge required-validation pass with SkipPostDecode.
//
// The annotation is reported wherever the author wrote it, including on
// a member of a oneof — a placement draft -01 §annotation-extensions
// ("Oneof Members") forbids outright, because read per field it demands
// that one specific arm always be chosen and so makes every other arm
// undecodable. [ValidateFile] rejects that schema as
// ViolationRequiredOption at bind time; this accessor reports what the
// schema author wrote, which is what tooling needs for diagnostics.
func IsRequired(fd protoreflect.FieldDescriptor) bool {
	return getBoolOption(fd, extRequired)
}

// Default returns the (pxf.default) value string if set. The string is
// a PXF literal (e.g. `42`, `true`, `"hello"`); callers parse it with
// [ApplyDefault] or their own logic. Exported for layered-config
// consumers running post-merge defaults passes.
//
// The annotation value is returned even when its placement is
// meaningless — a single literal cannot populate a repeated or map
// field. [ValidateFile] reports such placements as
// ViolationDefaultOption at bind time and [ApplyDefault] rejects those
// fds at runtime; this accessor reports what the schema author wrote,
// which is what tooling (protolsp, protocheck) needs for diagnostics.
//
// A caller applying the returned default itself owes the oneof rule of
// draft -01 §annotation-extensions ("Oneof Members"): if fd is a member
// of a non-synthetic oneof, the default MUST NOT be applied when any
// member of that oneof is present, because setting one member clears the
// rest and the default would destroy the chosen arm rather than shadow
// it (#72). postDecode gets this from its own presence map; a
// SkipPostDecode consumer has the same information in
// [Result.PresentFields] (a member bound to null counts as present) and
// must apply the same test. See [ApplyDefault].
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
			if len(b) < 4 {
				return false
			}
			b = b[4:]
		case protowire.Fixed64Type:
			if len(b) < 8 {
				return false
			}
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

// pxfOptions is every PXF annotation [ValidateFile] needs from one
// field. required has no "set" flag because only `= true` is actionable:
// an absent (pxf.required) and an explicit `= false` are the same thing
// to every caller, which is also what [IsRequired] reports.
type pxfOptions struct {
	key      string
	keySet   bool
	def      string
	defSet   bool
	required bool
}

// settled reports whether nothing further in fd's options could change
// the result, so a reader may stop where it stands. required needs no
// "set" flag here for the same reason it has none on the struct: once it
// is true, a later `= false` would not be actionable — while it is
// false, the scan must go on, since an unread `= true` still would be.
func (o pxfOptions) settled() bool { return o.keySet && o.defSet && o.required }

// pxfFieldOptions reads the (pxf.key), (pxf.default) and (pxf.required)
// extensions from fd's options in a single pass.
//
// [ValidateFile] runs per field on every decode that does not set
// SkipValidate, so the number of passes over FieldOptions sits on the
// hot path — one pass per field, not one per annotation. Reading two of
// them with two [getStringOption] calls measured +4.7% wall and +4
// allocs/op on BenchmarkPXFUnmarshalKeyed when the (pxf.default) check
// was added (#68): protoreflect's Range takes a capturing closure, which
// escapes, so the cost is per call and per field that carries any option
// at all. (pxf.required) joined the walker for #72 and rides along here
// rather than adding a third pass.
//
// The public accessors ([KeyFieldName], [Default], [IsRequired]) keep
// using getStringOption / getBoolOption — they are called one annotation
// at a time by tooling and do not need the combined shape.
//
// The accumulator is a plain local declared after the early return, not
// a named result. Range's closure captures it, so it is heap-bound
// wherever it is declared — as a named result that is every call,
// including the common one where the field carries no options at all,
// which measured +71% allocs/op on BenchmarkPXFUnmarshal. Declared here
// the cost lands only on fields that actually have options, which is
// what getStringOption has always done.
//
// Both loops stop once [pxfOptions.settled] holds: there is nothing left
// to learn, and neither loop is bounded by anything but the number of
// options the author wrote.
func pxfFieldOptions(fd protoreflect.FieldDescriptor) pxfOptions {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return pxfOptions{}
	}
	rm := opts.ProtoReflect()

	var got pxfOptions

	// Known fields first (protocompile stores resolved extensions here).
	rm.Range(func(ofd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch ofd.Number() {
		case extKey:
			got.key, got.keySet = v.String(), true
		case extDefault:
			got.def, got.defSet = v.String(), true
		case extRequired:
			got.required = v.Bool()
		}
		return !got.settled()
	})
	if got.settled() {
		return got
	}

	// Fallback: parse raw unknown bytes (protoc / descriptor-based). Fills
	// only what the known-field pass did not, and is skipped entirely when
	// there are no unknown bytes — the protocompile case.
	b := rm.GetUnknown()
	for len(b) > 0 && !got.settled() {
		fnum, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		switch wtype {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return got
			}
			if fnum == extRequired && !got.required {
				got.required = v != 0
			}
			b = b[vn:]
		case protowire.Fixed32Type:
			if len(b) < 4 {
				return got
			}
			b = b[4:]
		case protowire.Fixed64Type:
			if len(b) < 8 {
				return got
			}
			b = b[8:]
		case protowire.BytesType:
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return got
			}
			switch fnum {
			case extKey:
				if !got.keySet {
					got.key, got.keySet = string(v), true
				}
			case extDefault:
				if !got.defSet {
					got.def, got.defSet = string(v), true
				}
			}
			b = b[vn:]
		default:
			return got
		}
	}
	return got
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
			if len(b) < 4 {
				return "", false
			}
			b = b[4:]
		case protowire.Fixed64Type:
			if len(b) < 8 {
				return "", false
			}
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
