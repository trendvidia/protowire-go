// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// PXF schema-level conformance checks. Three families, all reported as
// [Violation] and all enforced at descriptor-bind time:
//
//   - Reserved names (draft-trendvidia-protowire-00 §3.13). A protobuf
//     schema bound for PXF use MUST NOT declare a message field, oneof,
//     or enum value whose name is case-sensitively equal to a PXF value
//     keyword (null / true / false): such a name lexes as the keyword,
//     so the declared element is unreachable from PXF surface syntax.
//   - (pxf.key) placement (draft -01 §3.13), see [checkKeyOption].
//   - (pxf.default) placement (draft -01 §annotation-extensions,
//     "Default Placement"), see [checkDefaultOption].
//
// Enforcement runs at descriptor-bind time inside [Unmarshal] /
// [UnmarshalDescriptor] / [UnmarshalFull] / [UnmarshalFullDescriptor].
// Callers that have already validated their descriptors (typically via
// [ValidateDescriptor] in a one-time codegen or registry-load pass) may
// set [UnmarshalOptions.SkipValidate] to bypass the per-call recheck.

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// reservedNames is the case-sensitive set of names that PXF reserves as
// value keywords and therefore forbids as schema element names
// (draft §3.13). The full reserved-directive-name set lives elsewhere
// (draft §3.4.6) and is enforced at the parser layer, not here:
// schema-element name collisions with directive names (e.g. a field
// literally named "dataset") are not problematic because field names
// and directive names live in disjoint lexical contexts.
var reservedNames = map[string]struct{}{
	"null":  {},
	"true":  {},
	"false": {},
}

// futureReservedDirectives is the set of directive names the spec
// reserves for future allocation (draft §3.4.6). v1 decoders MUST
// reject these as unknown reserved directives so applications cannot
// squat the names before the spec allocates semantics to them.
//
// The names with their own production (`type`, `dataset`, `proto`)
// don't appear here — they're handled directly by the lexer. The
// spec-registered `entry` doesn't appear either — it's a valid
// named_directive with documented shape (draft §3.4.3).
var futureReservedDirectives = map[string]struct{}{
	"table":       {},
	"datasource":  {},
	"view":        {},
	"procedure":   {},
	"function":    {},
	"permissions": {},
}

// ViolationKind identifies which kind of schema element collides with a
// reserved PXF value keyword.
type ViolationKind int

const (
	// ViolationField is a message field whose name is reserved.
	ViolationField ViolationKind = iota + 1
	// ViolationOneof is a oneof declaration whose name is reserved.
	ViolationOneof
	// ViolationEnumValue is an enum value whose name is reserved.
	ViolationEnumValue
	// ViolationKeyOption is a (pxf.key) annotation whose placement
	// violates draft -01 §3.13: the annotated field is not a repeated
	// message-typed field, or the annotation value does not name a
	// singular string field of the element message.
	ViolationKeyOption
	// ViolationDefaultOption is a (pxf.default) annotation whose
	// placement violates draft -01 §annotation-extensions ("Default
	// Placement"): the annotation carries exactly one PXF literal, and
	// the annotated field is one no single literal can denote — a
	// repeated field, a map field, a group, or a message-typed field
	// outside the set [applyMessageDefault] honors.
	ViolationDefaultOption
)

func (k ViolationKind) String() string {
	switch k {
	case ViolationField:
		return "message field"
	case ViolationOneof:
		return "oneof"
	case ViolationEnumValue:
		return "enum value"
	case ViolationKeyOption:
		return "keyed field option"
	case ViolationDefaultOption:
		return "default field option"
	default:
		return "unknown"
	}
}

// Violation describes one schema element that fails a PXF bind-time
// check: a name colliding with a reserved PXF keyword, or an invalid
// (pxf.key) or (pxf.default) placement. Returned by
// [ValidateDescriptor].
type Violation struct {
	// File is the .proto file path the offending element is declared in.
	File string
	// Element is the fully-qualified protobuf name of the element
	// (e.g. "trades.v1.Side.null").
	Element string
	// Name is the bare reserved identifier ("null" / "true" / "false")
	// for reserved-name violations, the (pxf.key) annotation value for
	// ViolationKeyOption, or the (pxf.default) literal for
	// ViolationDefaultOption.
	Name string
	// Kind is the kind of element that collided.
	Kind ViolationKind
	// Detail is a human-readable explanation; set only for
	// ViolationKeyOption and ViolationDefaultOption.
	Detail string
}

// String renders a one-line human-readable description of v.
func (v Violation) String() string {
	switch v.Kind {
	case ViolationKeyOption:
		return fmt.Sprintf("%s: field %q: invalid (pxf.key) = %q: %s (draft -01 §3.13)",
			v.File, v.Element, v.Name, v.Detail)
	case ViolationDefaultOption:
		return fmt.Sprintf("%s: field %q: invalid (pxf.default) = %q: %s (draft -01 §annotation-extensions)",
			v.File, v.Element, v.Name, v.Detail)
	}
	return fmt.Sprintf("%s: %s %q uses PXF-reserved name %q (draft §3.13)",
		v.File, v.Kind, v.Element, v.Name)
}

// ValidateDescriptor walks the file containing desc and returns every
// bind-time violation reachable from that file: reserved-name
// collisions among messages, oneofs, and enum values, plus invalid
// (pxf.key) placements (draft -01 §3.13). The returned slice is sorted
// by element fully-qualified name for stable output. A nil/empty slice
// means the schema is conformant.
//
// The check is case-sensitive: identifiers such as "NULL" or "True"
// lex as ordinary identifiers and are accepted.
func ValidateDescriptor(desc protoreflect.MessageDescriptor) []Violation {
	if desc == nil {
		return nil
	}
	return ValidateFile(desc.ParentFile())
}

// ValidateFile walks fd and returns every bind-time violation in the
// file. See [ValidateDescriptor] for the rules and semantics.
func ValidateFile(fd protoreflect.FileDescriptor) []Violation {
	if fd == nil {
		return nil
	}
	var out []Violation
	walkMessages(fd.Path(), fd.Messages(), &out)
	walkEnums(fd.Path(), fd.Enums(), &out)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Element < out[j].Element
	})
	return out
}

func walkMessages(path string, msgs protoreflect.MessageDescriptors, out *[]Violation) {
	for i := range msgs.Len() {
		md := msgs.Get(i)
		fields := md.Fields()
		for j := range fields.Len() {
			f := fields.Get(j)
			name := string(f.Name())
			if _, hit := reservedNames[name]; hit {
				*out = append(*out, Violation{
					File:    path,
					Element: string(f.FullName()),
					Name:    name,
					Kind:    ViolationField,
				})
			}
			checkKeyOption(path, f, out)
			checkDefaultOption(path, f, out)
		}
		oneofs := md.Oneofs()
		for j := range oneofs.Len() {
			o := oneofs.Get(j)
			if o.IsSynthetic() {
				continue
			}
			name := string(o.Name())
			if _, hit := reservedNames[name]; hit {
				*out = append(*out, Violation{
					File:    path,
					Element: string(o.FullName()),
					Name:    name,
					Kind:    ViolationOneof,
				})
			}
		}
		walkMessages(path, md.Messages(), out)
		walkEnums(path, md.Enums(), out)
	}
}

// checkKeyOption validates the placement of a (pxf.key) annotation on f
// per draft -01 §3.13: the annotated field must be a repeated
// message-typed field, and the annotation value must name a singular
// string field of the element message. Appends a ViolationKeyOption
// for each failure.
func checkKeyOption(path string, f protoreflect.FieldDescriptor, out *[]Violation) {
	keyName, ok := KeyFieldName(f)
	if !ok {
		return
	}
	violation := func(detail string) {
		*out = append(*out, Violation{
			File:    path,
			Element: string(f.FullName()),
			Name:    keyName,
			Kind:    ViolationKeyOption,
			Detail:  detail,
		})
	}
	if !f.IsList() || f.Kind() != protoreflect.MessageKind {
		violation("(pxf.key) is valid only on repeated message-typed fields")
		return
	}
	kf := f.Message().Fields().ByName(protoreflect.Name(keyName))
	if kf == nil {
		violation(fmt.Sprintf("element message %s has no field %q", f.Message().FullName(), keyName))
		return
	}
	if kf.IsList() || kf.IsMap() || kf.Kind() != protoreflect.StringKind {
		violation(fmt.Sprintf("key field %s must be a singular string field", kf.FullName()))
	}
}

// checkDefaultOption validates the placement of a (pxf.default)
// annotation on f per draft -01 §annotation-extensions ("Default
// Placement"): the annotation carries exactly one PXF literal, so it is
// valid only on fields a single literal can denote — singular scalars,
// enums, and the message types [applyMessageDefault] honors. Appends a
// ViolationDefaultOption for each failure.
//
// The rejected set is exactly the set [applyDefaultImpl] cannot honor, so
// a schema that binds here never meets a placement error at decode time
// and vice versa. Keep the two in lockstep: the message-type arm defers
// to [defaultableMessage], which is the same predicate
// applyMessageDefault's dispatch chain implements.
//
// Placement only — a literal that does not parse as the field's type
// ("abc" on an int32) stays a decode-time error. Placement is decidable
// from the descriptor alone; the literal is not, without running the
// value parser here.
func checkDefaultOption(path string, f protoreflect.FieldDescriptor, out *[]Violation) {
	def, ok := Default(f)
	if !ok {
		return
	}
	violation := func(detail string) {
		*out = append(*out, Violation{
			File:    path,
			Element: string(f.FullName()),
			Name:    def,
			Kind:    ViolationDefaultOption,
			Detail:  detail,
		})
	}
	// Map before list: a map field reports IsMap, not IsList, but the
	// ordering matches applyDefaultImpl's guard and reads the same way.
	switch {
	case f.IsMap():
		violation("(pxf.default) is not valid on map fields: one literal cannot denote a map")
		return
	case f.IsList():
		violation("(pxf.default) is not valid on repeated fields: one literal cannot denote a list")
		return
	}
	switch f.Kind() {
	case protoreflect.GroupKind:
		violation("(pxf.default) is not valid on group fields")
	case protoreflect.MessageKind:
		if !defaultableMessage(f.Message()) {
			violation(fmt.Sprintf("(pxf.default) is not valid on message type %s: no PXF literal denotes it",
				f.Message().FullName()))
		}
	}
}

func walkEnums(path string, enums protoreflect.EnumDescriptors, out *[]Violation) {
	for i := range enums.Len() {
		e := enums.Get(i)
		vs := e.Values()
		for j := range vs.Len() {
			v := vs.Get(j)
			name := string(v.Name())
			if _, hit := reservedNames[name]; hit {
				*out = append(*out, Violation{
					File:    path,
					Element: string(v.FullName()),
					Name:    name,
					Kind:    ViolationEnumValue,
				})
			}
		}
	}
}

// asValidationError joins a slice of violations into a single error
// suitable for returning from a decode call. Returns nil when vs is empty.
func asValidationError(vs []Violation) error {
	if len(vs) == 0 {
		return nil
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = v.String()
	}
	return fmt.Errorf("PXF schema violations:\n  %s", strings.Join(parts, "\n  "))
}
