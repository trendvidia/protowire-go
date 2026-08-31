// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
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

// IsRequired reports whether the field is required, in either of the two
// spellings: the bracket option `[(pxf.required) = true]`, or the
// annotation `@required` carried in the 1327 AnnotationList
// (see carrier.go). Both are honoured; neither is synthesized from the
// other.
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
	return pxfFieldOptions(fd).required
}

// Default returns the field's default value string if set, in either of
// the two spellings: the bracket option `[(pxf.default) = "…"]`, or the
// annotation `@default(v)` carried in the 1327 AnnotationList (see
// carrier.go), whose typed argument is reduced to the same literal form.
// The string is a PXF literal (e.g. `42`, `true`, `hello`); callers parse
// it with [ApplyDefault] or their own logic. Exported for layered-config
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
	o := pxfFieldOptions(fd)
	return o.def, o.defSet
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

// pxfOptions is every PXF annotation [ValidateFile] and [postDecode]
// need from one field, read from both spellings of the annotation
// surface. required has no "set" flag because only `= true` is
// actionable: an absent (pxf.required) and an explicit `= false` are the
// same thing to every caller, which is also what [IsRequired] reports.
type pxfOptions struct {
	key      string
	keySet   bool
	def      string
	defSet   bool
	required bool

	// Which spelling supplied def and required — see [annotSurface].
	defSurface annotSurface
	reqSurface annotSurface

	// defProblem, when non-empty, is why the field's default cannot be
	// applied: an @default the carrier holds that reduces to no single
	// literal, or two spellings of it that disagree. [walkMessages]
	// turns it into a ViolationDefaultOption. It is never silently
	// dropped — silent non-enforcement is the defect #81 is about.
	defProblem string
}

// pxfFieldOptions reads every PXF annotation on fd — the bracket-form
// extensions (pxf.key), (pxf.default) and (pxf.required), and the
// annotation-form @required / @default carried in the 1327
// AnnotationList (see carrier.go) — in a single pass over fd's options.
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
// [Default] and [IsRequired] are thin wrappers over this rather than
// readers of their own: with two spellings to consult, a per-annotation
// reader would duplicate the carrier walk once per accessor and could
// drift from it. [KeyFieldName] still uses [getStringOption]; (pxf.key)
// has no annotation-form spelling to consult.
//
// The accumulator is a plain local, not a named result. Range's closure captures it, so it is heap-bound
// wherever it is declared — as a named result that is every call,
// including the common one where the field carries no options at all,
// which measured +71% allocs/op on BenchmarkPXFUnmarshal. Declared here
// the cost lands only on fields that actually have options, which is
// what getStringOption has always done.
//
// Neither loop stops early any more. It used to break once key, default
// and required were all set, on the ground that nothing further could
// change the result — which the carrier makes false: a 1327 entry later
// in the same options blob still carries an @default nobody has read.
// The exit bounded a loop that is bounded anyway by the number of
// options the author wrote, and reading the carrier in this pass rather
// than a second one more than pays for it (see [postDecode], which used
// to make two passes and now makes one).
func pxfFieldOptions(fd protoreflect.FieldDescriptor) pxfOptions {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return pxfOptions{}
	}
	rm := opts.ProtoReflect()

	var got pxfOptions
	var carrier []byte

	// Known fields first (protocompile stores resolved extensions here).
	rm.Range(func(ofd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch ofd.Number() {
		case extKey:
			got.key, got.keySet = v.String(), true
		case extDefault:
			got.def, got.defSet = v.String(), true
		case extRequired:
			got.required = v.Bool()
		case extAnnotations:
			// The carrier is a message, so it is re-serialized for
			// [parseAnnotationList] to walk. Measured against
			// trendvidia/protocompile v0.25.0 this branch never fires —
			// the carrier arrives as unknown bytes even when the schema
			// imports descriptor.proto, because nothing registers the
			// extension in the Go type registry descriptorpb resolves
			// against. It fires in a process that has registered it, by
			// linking a generated package for
			// protowire/schema/v1/descriptor.proto.
			if ofd.Kind() == protoreflect.MessageKind {
				if b, err := proto.Marshal(v.Message().Interface()); err == nil {
					carrier = append(carrier, b...)
				}
			}
		}
		return true
	})

	// Fallback: parse raw unknown bytes (protoc / descriptor-based). Fills
	// only what the known-field pass did not, and is skipped entirely when
	// there are no unknown bytes.
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
				return got.withCarrier(carrier, fd)
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
			case extAnnotations:
				// Occurrences accumulate rather than the first
				// winning: a message-typed field split across several
				// merges under protobuf's rules, and AnnotationList
				// holds nothing but a repeated field, so concatenating
				// the payloads is that merge.
				carrier = append(carrier, v...)
			}
			b = b[vn:]
		default:
			return got.withCarrier(carrier, fd)
		}
	}
	return got.withCarrier(carrier, fd)
}

// withCarrier folds the annotation surface into the bracket surface o
// already holds.
//
// The two are not reconciled (RFC-001 §8.5): neither is synthesized from
// the other, and a value from one is never blended with a value from the
// other. What this binder does is honour both, which is exactly what
// "runtimes upgrade before schemas migrate" asks of it — the annotation
// form has no other enforcer, and routing it elsewhere would leave one
// semantic with two enforcers chosen by how the schema was spelled.
//
// required is one boolean with one answer, so either spelling asserting
// it makes the field required; there is no value to combine.
//
// default carries a value, so the two spellings can disagree. Applying
// either silently would be the reconciliation the draft forbids, and
// picking by precedence would attach meaning to which spelling an author
// migrated first — a detail they are free to change. The schema is
// rejected instead, which is the answer draft -01 already gives for two
// @defaults on one oneof, for the same reason. Equal values are not a
// disagreement and are not reported.
func (o pxfOptions) withCarrier(carrier []byte, fd protoreflect.FieldDescriptor) pxfOptions {
	if len(carrier) == 0 {
		return o
	}
	c := parseAnnotationList(carrier, fd)
	if c.required {
		if !o.required {
			o.reqSurface = surfaceCarrier
		}
		o.required = true
	}
	switch {
	case c.defProblem != "":
		// The unusable annotation is the carrier's, so the diagnostic
		// blames @default even when a bracket default sits beside it.
		o.defProblem, o.defSurface = c.defProblem, surfaceCarrier
	case !c.defSet:
		// no @default here
	case !o.defSet:
		o.def, o.defSet, o.defSurface = c.def, true, surfaceCarrier
	case o.def != c.def:
		o.defProblem = fmt.Sprintf(
			"(pxf.default) = %q and @default = %q carry different values, and the two surfaces must not be reconciled (RFC-001 §8.5)",
			o.def, c.def)
	}
	return o
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
