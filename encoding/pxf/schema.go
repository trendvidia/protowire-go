// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// PXF schema-level conformance checks. Four families, all reported as
// [Violation] and all enforced at descriptor-bind time:
//
//   - Reserved names (draft-trendvidia-protowire-00 §3.13). A protobuf
//     schema bound for PXF use MUST NOT declare a message field, oneof,
//     or enum value whose name is case-sensitively equal to a PXF value
//     keyword (null / true / false): such a name lexes as the keyword,
//     so the declared element is unreachable from PXF surface syntax.
//   - (pxf.key) placement (draft -01 §3.13), see [checkKeyOption].
//   - (pxf.default) placement (draft -01 §annotation-extensions,
//     "Default Placement"), see [checkDefaultOption]; plus the cap of
//     one (pxf.default) per oneof (same section, "Oneof Members"), see
//     [checkOneofDefaultCap].
//   - (pxf.required) placement (draft -01 §annotation-extensions,
//     "Oneof Members"): not valid on a member of a oneof at all.
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

// ViolationKind identifies which bind-time check an element failed:
// which kind of schema element collides with a reserved PXF value
// keyword, or which annotation is misplaced.
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
	// ViolationRequiredOption is a (pxf.required) annotation on a member
	// of a oneof, which draft -01 §annotation-extensions ("Oneof
	// Members") forbids: read per field it demands that one specific arm
	// always be chosen, which makes every other arm of the oneof
	// undecodable.
	ViolationRequiredOption
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
	case ViolationRequiredOption:
		return "required field option"
	default:
		return "unknown"
	}
}

// Violation describes one schema element that fails a PXF bind-time
// check: a name colliding with a reserved PXF keyword, or an invalid
// (pxf.key), (pxf.default) or (pxf.required) placement. Returned by
// [ValidateDescriptor].
type Violation struct {
	// File is the .proto file path the offending element is declared in.
	File string
	// Element is the fully-qualified protobuf name of the element
	// (e.g. "trades.v1.Side.null").
	Element string
	// Name is the bare reserved identifier ("null" / "true" / "false")
	// for reserved-name violations, the (pxf.key) annotation value for
	// ViolationKeyOption, the (pxf.default) literal for
	// ViolationDefaultOption, or the containing oneof's bare name for
	// ViolationRequiredOption. [Violation.String] does not render it in
	// the last case — Detail already names the oneof — so a caller
	// formatting violations itself should print one or the other, not
	// both.
	Name string
	// Kind is the kind of element that collided.
	Kind ViolationKind
	// Detail is a human-readable explanation; set for ViolationKeyOption,
	// ViolationDefaultOption and ViolationRequiredOption, empty for the
	// reserved-name kinds.
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
	case ViolationRequiredOption:
		return fmt.Sprintf("%s: field %q: invalid (pxf.required): %s (draft -01 §annotation-extensions)",
			v.File, v.Element, v.Detail)
	}
	return fmt.Sprintf("%s: %s %q uses PXF-reserved name %q (draft §3.13)",
		v.File, v.Kind, v.Element, v.Name)
}

// ValidateDescriptor walks the file containing desc and returns every
// bind-time violation declared in that file: reserved-name collisions
// among messages, oneofs, and enum values; invalid (pxf.key) placements
// (draft -01 §3.13); invalid (pxf.default) placements (draft -01
// §annotation-extensions, "Default Placement"); and the two oneof rules
// of that section's "Oneof Members" — at most one member of any one
// oneof may carry (pxf.default), and (pxf.required) is not valid on a
// oneof member at all. The returned slice is sorted by element
// fully-qualified name for stable output. A nil/empty slice means the
// schema is conformant.
//
// The reserved-name check is case-sensitive: identifiers such as "NULL"
// or "True" lex as ordinary identifiers and are accepted.
//
// Scope is desc's own file, not the transitive import closure: a
// violation on a message type declared in an imported .proto is not
// reported here, even when a field of desc refers to it. The decode-time
// guards in [ApplyDefault] and postDecode are what remain for those.
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
	// SliceStable, not Slice: one field can now yield up to four
	// violations sharing an Element (reserved name, (pxf.key),
	// (pxf.default) placement, and one of the two oneof rules), and only
	// a stable sort makes the documented "sorted for stable output" true
	// for those ties. walkMessages appends in a deterministic order, so
	// stability is enough — see [checkOneofDefaultCap].
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Element < out[j].Element
	})
	return out
}

func walkMessages(path string, msgs protoreflect.MessageDescriptors, out *[]Violation) {
	for i := range msgs.Len() {
		md := msgs.Get(i)
		fields := md.Fields()
		// Members of this message's oneofs that carry (pxf.default),
		// collected during the field pass and capped once the message's
		// fields are known. nil until some member has one, so the common
		// schema — no oneof defaults anywhere — never allocates.
		var oneofDefaults []oneofDefault
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
			// One options pass for every annotation — see
			// [pxfFieldOptions] for why this is not three calls.
			opts := pxfFieldOptions(f)
			if opts.keySet {
				checkKeyOption(path, f, opts.key, out)
			}
			if opts.defSet {
				checkDefaultOption(path, f, opts.def, out)
			}
			if oo := realOneof(f); oo != nil {
				if opts.required {
					*out = append(*out, Violation{
						File:    path,
						Element: string(f.FullName()),
						Name:    string(oo.Name()),
						Kind:    ViolationRequiredOption,
						Detail: fmt.Sprintf(
							"(pxf.required) is not valid on a member of oneof %q: read per field it demands that one arm always be chosen, which makes every other arm undecodable",
							oo.Name()),
					})
				}
				if opts.defSet {
					oneofDefaults = append(oneofDefaults, oneofDefault{oneof: oo, field: f, def: opts.def})
				}
			}
		}
		checkOneofDefaultCap(path, oneofDefaults, out)
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

// oneofDefault is one oneof member carrying (pxf.default), recorded
// during walkMessages' field pass. def rides along because
// [pxfFieldOptions] already read it — re-deriving it with [Default]
// would spend a second full pass over the field's options, which is the
// cost pxfFieldOptions exists to avoid.
type oneofDefault struct {
	oneof protoreflect.OneofDescriptor
	field protoreflect.FieldDescriptor
	def   string
}

// checkOneofDefaultCap reports every member of a oneof that carries
// (pxf.default) when a sibling carries one too: at most one member of
// any one oneof may carry it (draft -01 §annotation-extensions, "Oneof
// Members"). With two, some default must win, and deciding by
// declaration order or field number would attach meaning to a detail
// authors are free to change — so the schema is rejected instead.
//
// Reported on every offending member rather than once on the oneof, so
// the author sees each annotation to remove and [Violation.String] stays
// a field-shaped message.
//
// annotated holds one entry per annotated oneof member of a single
// message in field-declaration order; entries for one oneof need not be
// adjacent, so grouping needs the map. Groups are emitted at their first
// member's position, which keeps the appended order independent of map
// iteration order.
func checkOneofDefaultCap(path string, annotated []oneofDefault, out *[]Violation) {
	if len(annotated) < 2 {
		return // no oneof in this message can hold two
	}
	byOneof := make(map[protoreflect.FullName][]oneofDefault, len(annotated))
	for _, a := range annotated {
		byOneof[a.oneof.FullName()] = append(byOneof[a.oneof.FullName()], a)
	}
	for _, a := range annotated {
		members := byOneof[a.oneof.FullName()]
		if len(members) < 2 || members[0].field.Number() != a.field.Number() {
			continue // conformant, or already reported from its first member
		}
		names := make([]string, len(members))
		for k, m := range members {
			names[k] = string(m.field.Name())
		}
		detail := fmt.Sprintf(
			"at most one member of oneof %q may carry (pxf.default); %d do (%s)",
			a.oneof.Name(), len(members), strings.Join(names, ", "))
		for _, m := range members {
			*out = append(*out, Violation{
				File:    path,
				Element: string(m.field.FullName()),
				Name:    m.def,
				Kind:    ViolationDefaultOption,
				Detail:  detail,
			})
		}
	}
}

// realOneof returns f's containing oneof, or nil when f is not a member
// of one. A proto3 `optional` field sits in a synthetic single-member
// oneof, which carries none of the semantics the oneof rules are about —
// nothing else can clear it — so it reports nil, and such fields keep
// plain per-field presence.
func realOneof(f protoreflect.FieldDescriptor) protoreflect.OneofDescriptor {
	oo := f.ContainingOneof()
	if oo == nil || oo.IsSynthetic() {
		return nil
	}
	return oo
}

// checkKeyOption validates the placement of a (pxf.key) annotation on f
// per draft -01 §3.13: the annotated field must be a repeated
// message-typed field, and the annotation value must name a singular
// string field of the element message. Appends a ViolationKeyOption
// for each failure.
//
// keyName is the authored annotation value, already read by the caller;
// callers must only invoke this when the annotation is actually set.
func checkKeyOption(path string, f protoreflect.FieldDescriptor, keyName string, out *[]Violation) {
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
// the two guards agree on which placements are bad. Keep them in
// lockstep: the message-type arm defers to [defaultableMessage], which is
// the same predicate applyMessageDefault's dispatch chain implements.
//
// Agreeing on the set is not the same as covering the same fields.
// [ValidateFile] walks one file, while postDecode recurses into nested
// messages, so a bad placement on a type declared in an imported .proto
// still reaches the decode-time guard unreported.
//
// Placement only — a literal that does not parse as the field's type
// ("abc" on an int32) stays a decode-time error. Placement is decidable
// from the descriptor alone; the literal is not, without running the
// value parser here.
//
// def is the authored annotation value, already read by the caller;
// callers must only invoke this when the annotation is actually set.
func checkDefaultOption(path string, f protoreflect.FieldDescriptor, def string, out *[]Violation) {
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
