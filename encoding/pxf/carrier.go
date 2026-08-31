// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// The schema-extension annotation carrier (RFC-001 §8.1).
//
// PXF has two spellings for the same two semantics. The bracket form
// `[(pxf.required) = true]` / `[(pxf.default) = "…"]` lowers to the
// extension numbers 1314 / 1315 that [annotations.go] reads. The
// annotation form `@required` / `@default(v)` — "the canonical
// annotation form going forward" (RFC-001 §8.5) — lowers to something
// else entirely: one AnnotationList riding extension number 1327 on the
// field's options, holding one entry per `@` use site.
//
// The two surfaces are disjoint by construction. A compiler emits the
// one the author wrote and MUST NOT synthesize the other (RFC-001 §8.5,
// draft -01), so a runtime that reads only 1314/1315 does not see
// `@required` at all — it binds a migrated schema without error and
// without enforcement. That was protowire-go until #81: the reason the
// gap is silent is the same rule that makes it a runtime's job to close.
//
// This file reads the carrier's wire bytes directly rather than binding
// protowire/schema/v1/descriptor.proto as a Go dependency. The shape it
// walks is small and frozen by STABILITY.md promise 3, and the library
// packages must reach no .proto compiler and no generated schema package
// (pinned by internal/deps) — a descriptor for the carrier would be one.

import (
	"encoding/base64"
	"math"
	"strconv"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// extAnnotations is the annotation carrier's extension number. All eight
// kind-specific carriers share it — `file_annotations` on FileOptions,
// `field_annotations` on FieldOptions, and so on — because the per-kind
// names exist only to give each extension a unique fully-qualified name
// inside one proto package, not to give it a distinct wire number.
const extAnnotations protoreflect.FieldNumber = 1327

// The two annotations this binder acts on, by the fully-qualified name
// the lowering pass writes into Annotation.name. Declared in
// protowire/proto/schema/v1/annotations.proto.
const (
	annotationRequired = "protowire.schema.v1.required"
	annotationDefault  = "protowire.schema.v1.default"
)

// Field numbers inside the carrier, from
// protowire/proto/schema/v1/descriptor.proto. Named rather than inlined
// because the walk below is the only reader and its correctness is
// entirely in these numbers matching that file.
const (
	fnListEntries = 1 // AnnotationList.entries

	fnAnnotationName = 1 // Annotation.name
	fnAnnotationArgs = 2 // Annotation.args

	fnArgName        = 1  // AnnotationArg.name
	fnArgStringValue = 10 // AnnotationArg.string_value
	fnArgIntValue    = 11 // AnnotationArg.int_value
	fnArgDoubleValue = 12 // AnnotationArg.double_value
	fnArgBoolValue   = 13 // AnnotationArg.bool_value
	fnArgBytesValue  = 14 // AnnotationArg.bytes_value
	fnArgLiteral     = 15 // AnnotationArg.literal

	fnLiteralEnumValue = 1 // Literal.enum_value
	fnEnumValueName    = 2 // EnumLiteral.value_name
)

// defaultParamName is the name of `annotation default(value: any)`'s
// single parameter. `@default("x")` writes the argument positionally and
// `@default(value = "x")` names it; both lower to one AnnotationArg, the
// named form additionally setting AnnotationArg.name.
const defaultParamName = "value"

// carrierOptions is what the 1327 carrier says about one field: the
// subset of the annotation surface this binder enforces. Annotations it
// does not act on (@validate, @description, …) are skipped — they belong
// to protocheck and to the documentation generators.
type carrierOptions struct {
	// required is true when the field carries @required.
	required bool
	// def is @default's argument reduced to the same PXF literal string
	// the bracket form carries, so everything downstream — placement
	// checks, the oneof cap, [ApplyDefault] — is shared between the two
	// surfaces rather than reimplemented per spelling.
	def    string
	defSet bool
	// defProblem, when non-empty, says why an @default the field does
	// carry could not be reduced to a literal — a list argument, a
	// message literal, an argument the lowering pass left empty.
	// [walkMessages] turns it into a ViolationDefaultOption rather than
	// dropping the annotation, which would be the silent non-enforcement
	// #81 exists to end.
	defProblem string
}

// annotSurface names which of PXF's two spellings supplied an
// annotation, so a diagnostic quotes the form the author wrote rather
// than the one this binder happens to read first. Being told to remove
// an option the schema does not contain is the specific way a
// two-surface feature wastes someone's afternoon.
//
// surfaceOption is the zero value, so a [Violation] a caller builds
// itself renders the bracket spelling, as it did before the carrier
// existed.
type annotSurface uint8

const (
	surfaceOption  annotSurface = iota // [(pxf.required) = true], [(pxf.default) = "…"]
	surfaceCarrier                     // @required, @default(…)
)

func (s annotSurface) requiredName() string {
	if s == surfaceCarrier {
		return "@required"
	}
	return "(pxf.required)"
}

func (s annotSurface) defaultName() string {
	if s == surfaceCarrier {
		return "@default"
	}
	return "(pxf.default)"
}

// parseAnnotationList walks the carrier's entries and picks out the two
// annotations this binder enforces. Unparseable bytes stop the walk and
// keep whatever was read before them: the alternative is to report a
// schema as unannotated because its last entry was truncated, which is
// the silent non-enforcement this whole path exists to remove.
func parseAnnotationList(b []byte, fd protoreflect.FieldDescriptor) carrierOptions {
	var got carrierOptions
	for len(b) > 0 {
		num, _, _, v, n := nextField(b)
		if n < 0 {
			break
		}
		if num == fnListEntries && v != nil {
			got.applyAnnotation(v, fd)
		}
		b = b[n:]
	}
	return got
}

// applyAnnotation folds one Annotation entry into got. A truncated entry
// contributes whatever it did name, on the same reasoning as
// [parseAnnotationList]: dropping a @required whose name was read
// because a later byte was garbled is silent non-enforcement, which is
// the failure this path exists to remove.
func (got *carrierOptions) applyAnnotation(b []byte, fd protoreflect.FieldDescriptor) {
	var name string
	var args [][]byte
	rest := b
	for len(rest) > 0 {
		num, _, _, v, n := nextField(rest)
		if n < 0 {
			break
		}
		switch {
		case num == fnAnnotationName && v != nil:
			name = string(v)
		case num == fnAnnotationArgs && v != nil:
			args = append(args, v)
		}
		rest = rest[n:]
	}

	switch name {
	case annotationRequired:
		got.required = true
	case annotationDefault:
		if got.defSet || got.defProblem != "" {
			return // first @default wins; a second is the compiler's to reject
		}
		arg := defaultArg(args)
		if arg == nil {
			got.defProblem = "@default carries no value argument"
			return
		}
		lit, problem := argLiteral(arg, fd)
		if problem != "" {
			got.defProblem = problem
			return
		}
		got.def, got.defSet = lit, true
	}
}

// defaultArg picks `default`'s single `value` argument out of args: the
// first positional one, or the first named `value`. Anything else is an
// argument list the compiler should have rejected against the
// declaration, so it is ignored rather than guessed at.
func defaultArg(args [][]byte) []byte {
	for _, a := range args {
		switch argName(a) {
		case "", defaultParamName:
			return a
		}
	}
	return nil
}

// argName returns an AnnotationArg's name, empty for a positional one.
func argName(b []byte) string {
	for len(b) > 0 {
		num, _, _, v, n := nextField(b)
		if n < 0 {
			return ""
		}
		if num == fnArgName && v != nil {
			return string(v)
		}
		b = b[n:]
	}
	return ""
}

// argLiteral reduces one AnnotationArg to the PXF literal string the
// bracket form would have carried, so that both surfaces hand
// [ApplyDefault] the same thing. Returns a problem string instead for an
// argument no single literal can denote.
//
// fd decides one case the wire bytes cannot: AnnotationArg has no
// unsigned variant, and the lowering pass reinterprets a large unsigned
// literal through int64 to preserve its bits, so 18446744073709551615
// arrives as int_value -1. Reading it back as unsigned is required to
// recover the literal the author wrote, and is not a reconciliation of
// the two surfaces — it is the documented encoding of one of them.
func argLiteral(b []byte, fd protoreflect.FieldDescriptor) (lit string, problem string) {
	for len(b) > 0 {
		num, typ, u, v, n := nextField(b)
		if n < 0 {
			return "", "@default's value is not well-formed"
		}
		switch num {
		case fnArgStringValue:
			if v != nil {
				return string(v), ""
			}
		case fnArgIntValue:
			if typ == protowire.VarintType {
				if unsignedTarget(fd) {
					return strconv.FormatUint(u, 10), ""
				}
				return strconv.FormatInt(int64(u), 10), ""
			}
		case fnArgDoubleValue:
			if typ == protowire.Fixed64Type {
				return strconv.FormatFloat(math.Float64frombits(u), 'g', -1, 64), ""
			}
		case fnArgBoolValue:
			if typ == protowire.VarintType {
				return strconv.FormatBool(u != 0), ""
			}
		case fnArgBytesValue:
			if v != nil {
				// The bracket form spells a bytes default as bare
				// base64 ("AQID"), which is what [ApplyDefault] parses.
				return base64.StdEncoding.EncodeToString(v), ""
			}
		case fnArgLiteral:
			if v != nil {
				return literalValue(v)
			}
		}
		b = b[n:]
	}
	return "", "@default's value is empty"
}

// literalValue reduces a Literal to a PXF literal. Only the resolved
// enum reference has one: a message literal and a list literal denote
// values a single literal cannot, and draft -01 §annotation-extensions
// ("Default Placement") already rejects the fields they could target.
func literalValue(b []byte) (lit string, problem string) {
	for len(b) > 0 {
		num, _, _, v, n := nextField(b)
		if n < 0 {
			return "", "@default's value is not well-formed"
		}
		if num == fnLiteralEnumValue && v != nil {
			if name := enumValueName(v); name != "" {
				return name, ""
			}
			return "", "@default names an enum value the lowering pass left unresolved"
		}
		b = b[n:]
	}
	return "", "@default carries a message or list literal, and a default is exactly one literal"
}

// enumValueName returns an EnumLiteral's resolved value name. The
// lowering pass resolves the reference, so consumers never re-resolve a
// bare name against a descriptor pool.
func enumValueName(b []byte) string {
	for len(b) > 0 {
		num, _, _, v, n := nextField(b)
		if n < 0 {
			return ""
		}
		if num == fnEnumValueName && v != nil {
			return string(v)
		}
		b = b[n:]
	}
	return ""
}

// unsignedTarget reports whether fd's default literal is read as an
// unsigned integer — the unsigned scalar kinds, and the two wrapper
// types that wrap them.
func unsignedTarget(fd protoreflect.FieldDescriptor) bool {
	switch fd.Kind() {
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return true
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case "google.protobuf.UInt32Value", "google.protobuf.UInt64Value":
			return true
		}
	}
	return false
}

// nextField consumes one field from b. u carries the payload of a
// varint, fixed32 or fixed64 field, v the payload of a length-delimited
// one (nil otherwise), and n the bytes consumed — negative when b does
// not begin a well-formed field. Group wire types are not produced by
// the lowering pass and are treated as malformed.
func nextField(b []byte) (num protowire.Number, typ protowire.Type, u uint64, v []byte, n int) {
	num, typ, tagLen := protowire.ConsumeTag(b)
	if tagLen < 0 {
		return 0, 0, 0, nil, -1
	}
	rest := b[tagLen:]
	switch typ {
	case protowire.VarintType:
		val, vn := protowire.ConsumeVarint(rest)
		if vn < 0 {
			return 0, 0, 0, nil, -1
		}
		return num, typ, val, nil, tagLen + vn
	case protowire.Fixed32Type:
		val, vn := protowire.ConsumeFixed32(rest)
		if vn < 0 {
			return 0, 0, 0, nil, -1
		}
		return num, typ, uint64(val), nil, tagLen + vn
	case protowire.Fixed64Type:
		val, vn := protowire.ConsumeFixed64(rest)
		if vn < 0 {
			return 0, 0, 0, nil, -1
		}
		return num, typ, val, nil, tagLen + vn
	case protowire.BytesType:
		val, vn := protowire.ConsumeBytes(rest)
		if vn < 0 {
			return 0, 0, 0, nil, -1
		}
		return num, typ, 0, val, tagLen + vn
	}
	return 0, 0, 0, nil, -1
}
