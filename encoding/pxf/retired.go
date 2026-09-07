// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Retired option numbers.
//
// Before protowire v1.12.0 the family's extensions lived in an
// unregistered 50000–59999 range it had squatted; they moved to the
// registered block 1314–1363 in one coordinated change (protowire#244),
// and the old numbers are dead: STABILITY.md promise 3 names them so
// that a descriptor compiled before the move is DIAGNOSABLE rather than
// merely unknown, and draft -01 says an implementation encountering
// them SHOULD diagnose the descriptor as pre-registration rather than
// interpret it (#98). Without this, such a descriptor fails silently:
// (pxf.required) stops being enforced, (pxf.default) stops applying,
// and the schema binds documents it should reject.
//
// The check is scoped two ways, because 50000-and-up is where every
// project that never registered its numbers puts them: a retired number
// counts only on the Options kind the retired allocation used, and only
// in a file that imports one of protowire's own annotation files — the
// import a schema needs in order to have written the option at all. A
// third party's `50000` on a FieldOptions in a file that never heard of
// protowire is that party's business.

// retiredNumber is what a retired option number used to be and where
// it went.
type retiredNumber struct {
	was  string           // the option as it was spelled
	now  protowire.Number // its number in the registered block
	file string           // the annotations file to recompile against
}

var (
	retiredFileOptions = map[protowire.Number]retiredNumber{
		50100: {"(sbe.schema_id)", 1319, "sbe/annotations.proto"},
		50101: {"(sbe.version)", 1320, "sbe/annotations.proto"},
		50400: {"the file_annotations carrier", 1327, "protowire/schema/v1/descriptor.proto"},
		50401: {"the functions carrier", 1328, "protowire/schema/v1/descriptor.proto"},
		50402: {"the annotation_decls carrier", 1329, "protowire/schema/v1/descriptor.proto"},
		50403: {"the type_decls carrier", 1330, "protowire/schema/v1/descriptor.proto"},
		50404: {"the source_map carrier", 1331, "protowire/schema/v1/descriptor.proto"},
	}
	retiredMessageOptions = map[protowire.Number]retiredNumber{
		50200: {"(sbe.template_id)", 1321, "sbe/annotations.proto"},
		50400: {"the message_annotations carrier", 1327, "protowire/schema/v1/descriptor.proto"},
		// protocheck's kinds are inferred from the registered names
		// (field / message / oneof); its declarations are not in the
		// protowire repository.
		51001: {"a protocheck message constraint", 1348, "protocheck's annotations"},
	}
	retiredFieldOptions = map[protowire.Number]retiredNumber{
		50000: {"(pxf.required)", 1314, "pxf/annotations.proto"},
		50001: {"(pxf.default)", 1315, "pxf/annotations.proto"},
		50002: {"(pxf.key)", 1316, "pxf/annotations.proto"},
		50300: {"(sbe.length)", 1322, "sbe/annotations.proto"},
		50301: {"(sbe.encoding)", 1323, "sbe/annotations.proto"},
		50400: {"the field_annotations carrier", 1327, "protowire/schema/v1/descriptor.proto"},
		51000: {"a protocheck field constraint", 1347, "protocheck's annotations"},
	}
	retiredOneofOptions = map[protowire.Number]retiredNumber{
		50400: {"the oneof_annotations carrier", 1327, "protowire/schema/v1/descriptor.proto"},
		51002: {"a protocheck oneof constraint", 1349, "protocheck's annotations"},
	}
	// Enum, enum value, service and method options carried only the
	// annotation carrier.
	retiredCarrierOnly = map[protowire.Number]retiredNumber{
		50400: {"the *_annotations carrier", 1327, "protowire/schema/v1/descriptor.proto"},
	}
)

// protowireAnnotationFiles are the imports that mark a file as having
// been compiled against protowire's annotations, in either era.
var protowireAnnotationFiles = map[string]bool{
	"pxf/annotations.proto":                 true,
	"sbe/annotations.proto":                 true,
	"protowire/schema/v1/annotations.proto": true,
	"protowire/schema/v1/descriptor.proto":  true,
}

// importsProtowire reports whether fd imports one of protowire's
// annotation files. Placeholders count: a descriptor set loaded without
// its imports still names them.
func importsProtowire(fd protoreflect.FileDescriptor) bool {
	imps := fd.Imports()
	for i := range imps.Len() {
		if protowireAnnotationFiles[imps.Get(i).Path()] {
			return true
		}
	}
	return false
}

// walkRetired appends a violation for every retired option number the
// file's elements carry. Only unknown bytes are read: nothing declares
// the retired numbers any more, so that is where they land.
func walkRetired(path string, fd protoreflect.FileDescriptor, out *[]Violation) {
	retiredIn(path, path, "FileOptions", fd.Options(), retiredFileOptions, out)
	walkRetiredMessages(path, fd.Messages(), out)
	walkRetiredEnums(path, fd.Enums(), out)
	svcs := fd.Services()
	for i := range svcs.Len() {
		s := svcs.Get(i)
		retiredIn(path, string(s.FullName()), "ServiceOptions", s.Options(), retiredCarrierOnly, out)
		ms := s.Methods()
		for j := range ms.Len() {
			m := ms.Get(j)
			retiredIn(path, string(m.FullName()), "MethodOptions", m.Options(), retiredCarrierOnly, out)
		}
	}
}

func walkRetiredMessages(path string, msgs protoreflect.MessageDescriptors, out *[]Violation) {
	for i := range msgs.Len() {
		md := msgs.Get(i)
		retiredIn(path, string(md.FullName()), "MessageOptions", md.Options(), retiredMessageOptions, out)
		fields := md.Fields()
		for j := range fields.Len() {
			f := fields.Get(j)
			retiredIn(path, string(f.FullName()), "FieldOptions", f.Options(), retiredFieldOptions, out)
		}
		oneofs := md.Oneofs()
		for j := range oneofs.Len() {
			o := oneofs.Get(j)
			retiredIn(path, string(o.FullName()), "OneofOptions", o.Options(), retiredOneofOptions, out)
		}
		walkRetiredEnums(path, md.Enums(), out)
		walkRetiredMessages(path, md.Messages(), out)
	}
}

func walkRetiredEnums(path string, enums protoreflect.EnumDescriptors, out *[]Violation) {
	for i := range enums.Len() {
		e := enums.Get(i)
		retiredIn(path, string(e.FullName()), "EnumOptions", e.Options(), retiredCarrierOnly, out)
		vals := e.Values()
		for j := range vals.Len() {
			v := vals.Get(j)
			retiredIn(path, string(v.FullName()), "EnumValueOptions", v.Options(), retiredCarrierOnly, out)
		}
	}
}

// retiredIn scans one Options message's unknown bytes for the numbers
// in table and appends a violation for each hit. opts may be a typed nil
// (an element with no options at all), which has no unknown bytes.
func retiredIn(path, element, kind string, opts proto.Message, table map[protowire.Number]retiredNumber, out *[]Violation) {
	if opts == nil {
		return
	}
	rm := opts.ProtoReflect()
	if !rm.IsValid() {
		return
	}
	b := rm.GetUnknown()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		vn := protowire.ConsumeFieldValue(num, typ, b[n:])
		if vn < 0 {
			return
		}
		b = b[n+vn:]
		r, hit := table[num]
		if !hit {
			continue
		}
		*out = append(*out, Violation{
			File:    path,
			Element: element,
			Name:    fmt.Sprint(num),
			Kind:    ViolationRetiredNumber,
			Detail: fmt.Sprintf("option number %d on %s was %s before protowire v1.12.0 and is %d now; "+
				"the descriptor predates the registered extension block and must be recompiled against the current %s",
				num, kind, r.was, r.now, r.file),
		})
	}
}
