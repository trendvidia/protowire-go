// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// PXF schema-level conformance check per draft-trendvidia-protowire-00
// §3.13. A protobuf schema bound for PXF use MUST NOT declare a message
// field, oneof, or enum value whose name is case-sensitively equal to a
// PXF value keyword (null / true / false): such a name lexes as the
// keyword, so the declared element is unreachable from PXF surface syntax.
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
// value keywords and therefore forbids as schema element names.
var reservedNames = map[string]struct{}{
	"null":  {},
	"true":  {},
	"false": {},
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
)

func (k ViolationKind) String() string {
	switch k {
	case ViolationField:
		return "message field"
	case ViolationOneof:
		return "oneof"
	case ViolationEnumValue:
		return "enum value"
	default:
		return "unknown"
	}
}

// Violation describes one schema element whose name collides with a
// reserved PXF keyword. Returned by [ValidateDescriptor].
type Violation struct {
	// File is the .proto file path the offending element is declared in.
	File string
	// Element is the fully-qualified protobuf name of the element
	// (e.g. "trades.v1.Side.null").
	Element string
	// Name is the bare reserved identifier ("null" / "true" / "false").
	Name string
	// Kind is the kind of element that collided.
	Kind ViolationKind
}

// String renders a one-line human-readable description of v.
func (v Violation) String() string {
	return fmt.Sprintf("%s: %s %q uses PXF-reserved name %q (draft §3.13)",
		v.File, v.Kind, v.Element, v.Name)
}

// ValidateDescriptor walks the file containing desc and returns every
// reserved-name collision among messages, oneofs, and enum values
// reachable from that file. The returned slice is sorted by element
// fully-qualified name for stable output. A nil/empty slice means the
// schema is conformant.
//
// The check is case-sensitive: identifiers such as "NULL" or "True"
// lex as ordinary identifiers and are accepted.
func ValidateDescriptor(desc protoreflect.MessageDescriptor) []Violation {
	if desc == nil {
		return nil
	}
	return ValidateFile(desc.ParentFile())
}

// ValidateFile walks fd and returns every reserved-name collision in
// the file. See [ValidateDescriptor] for the rule and semantics.
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
	return fmt.Errorf("PXF schema reserved-name violations:\n  %s", strings.Join(parts, "\n  "))
}
