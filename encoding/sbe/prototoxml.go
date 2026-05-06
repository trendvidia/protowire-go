// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ProtoToXML converts proto file descriptors with SBE annotations to an SBE XML schema.
func ProtoToXML(files ...protoreflect.FileDescriptor) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("sbe: no files provided")
	}

	fd := files[0]

	schemaID, ok := getFileUint32Option(fd, extSchemaID)
	if !ok {
		return nil, fmt.Errorf("sbe: file %s missing (sbe.schema_id)", fd.Path())
	}
	version, _ := getFileUint32Option(fd, extVersion)

	// Pre-collect types needed for the <types> section.
	strLengths := make(map[uint32]bool)
	var composites []protoreflect.MessageDescriptor
	compSeen := make(map[protoreflect.FullName]bool)
	var enums []protoreflect.EnumDescriptor
	enumSeen := make(map[protoreflect.FullName]bool)

	// Top-level enums.
	fileEnums := fd.Enums()
	for i := 0; i < fileEnums.Len(); i++ {
		ed := fileEnums.Get(i)
		enums = append(enums, ed)
		enumSeen[ed.FullName()] = true
	}

	// Walk all messages.
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		if _, ok := getMessageUint32Option(md, extTemplateID); ok {
			collectXMLTypes(md, strLengths, &composites, compSeen, &enums, enumSeen)
		} else {
			// Non-template top-level message → composite.
			if !compSeen[md.FullName()] {
				compSeen[md.FullName()] = true
				composites = append(composites, md)
			}
		}
	}

	// Sort string lengths for deterministic output.
	var lengths []uint32
	for l := range strLengths {
		lengths = append(lengths, l)
	}
	sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })

	var b strings.Builder

	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&b, "<sbe:messageSchema xmlns:sbe=\"http://fixprotocol.io/2016/sbe\"\n")
	fmt.Fprintf(&b, "                   package=\"%s\"\n", string(fd.Package()))
	fmt.Fprintf(&b, "                   id=\"%d\"\n", schemaID)
	fmt.Fprintf(&b, "                   version=\"%d\"\n", version)
	fmt.Fprintf(&b, "                   byteOrder=\"littleEndian\">\n")

	// Types section.
	b.WriteString("    <types>\n")

	// Standard composites.
	b.WriteString("        <composite name=\"messageHeader\">\n")
	b.WriteString("            <type name=\"blockLength\" primitiveType=\"uint16\"/>\n")
	b.WriteString("            <type name=\"templateId\" primitiveType=\"uint16\"/>\n")
	b.WriteString("            <type name=\"schemaId\" primitiveType=\"uint16\"/>\n")
	b.WriteString("            <type name=\"version\" primitiveType=\"uint16\"/>\n")
	b.WriteString("        </composite>\n")
	b.WriteString("        <composite name=\"groupSizeEncoding\">\n")
	b.WriteString("            <type name=\"blockLength\" primitiveType=\"uint16\"/>\n")
	b.WriteString("            <type name=\"numInGroup\" primitiveType=\"uint16\"/>\n")
	b.WriteString("        </composite>\n")

	// Named string types.
	for _, l := range lengths {
		fmt.Fprintf(&b, "        <type name=\"str%d\" primitiveType=\"char\" length=\"%d\"/>\n", l, l)
	}

	// Enums.
	for _, ed := range enums {
		p2xWriteEnum(&b, ed)
	}

	// Composites.
	for _, md := range composites {
		p2xWriteComposite(&b, md)
	}

	b.WriteString("    </types>\n")

	// Messages.
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		if tid, ok := getMessageUint32Option(md, extTemplateID); ok {
			p2xWriteMessage(&b, md, tid)
		}
	}

	b.WriteString("</sbe:messageSchema>\n")

	return []byte(b.String()), nil
}

func collectXMLTypes(md protoreflect.MessageDescriptor, strLengths map[uint32]bool,
	composites *[]protoreflect.MessageDescriptor, compSeen map[protoreflect.FullName]bool,
	enums *[]protoreflect.EnumDescriptor, enumSeen map[protoreflect.FullName]bool) {

	// Nested enums.
	nestedEnums := md.Enums()
	for i := 0; i < nestedEnums.Len(); i++ {
		ed := nestedEnums.Get(i)
		if !enumSeen[ed.FullName()] {
			enumSeen[ed.FullName()] = true
			*enums = append(*enums, ed)
		}
	}

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)

		if f.Kind() == protoreflect.StringKind || f.Kind() == protoreflect.BytesKind {
			if length, ok := getFieldUint32Option(f, extLength); ok {
				strLengths[length] = true
			}
		}

		if f.Kind() == protoreflect.EnumKind {
			ed := f.Enum()
			if !enumSeen[ed.FullName()] {
				enumSeen[ed.FullName()] = true
				*enums = append(*enums, ed)
			}
		}

		if f.Kind() == protoreflect.MessageKind {
			if f.IsList() {
				collectXMLTypes(f.Message(), strLengths, composites, compSeen, enums, enumSeen)
			} else {
				msgDesc := f.Message()
				if !compSeen[msgDesc.FullName()] {
					compSeen[msgDesc.FullName()] = true
					*composites = append(*composites, msgDesc)
					collectXMLTypes(msgDesc, strLengths, composites, compSeen, enums, enumSeen)
				}
			}
		}
	}
}

func p2xWriteEnum(b *strings.Builder, ed protoreflect.EnumDescriptor) {
	name := string(ed.Name())
	fmt.Fprintf(b, "        <enum name=\"%s\" encodingType=\"uint8\">\n", name)
	values := ed.Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		valueName := stripEnumPrefix(string(v.Name()), name)
		fmt.Fprintf(b, "            <validValue name=\"%s\">%d</validValue>\n", valueName, v.Number())
	}
	fmt.Fprintf(b, "        </enum>\n")
}

func p2xWriteComposite(b *strings.Builder, md protoreflect.MessageDescriptor) {
	name := string(md.Name())
	fmt.Fprintf(b, "        <composite name=\"%s\">\n", name)
	sorted := sortedFields(md)
	for _, f := range sorted {
		fieldName := snakeToCamel(string(f.Name()))
		info := protoFieldToSBEType(f)
		if info.length > 0 {
			fmt.Fprintf(b, "            <type name=\"%s\" primitiveType=\"%s\" length=\"%d\"/>\n", fieldName, info.primitiveType, info.length)
		} else {
			fmt.Fprintf(b, "            <type name=\"%s\" primitiveType=\"%s\"/>\n", fieldName, info.primitiveType)
		}
	}
	fmt.Fprintf(b, "        </composite>\n")
}

func p2xWriteMessage(b *strings.Builder, md protoreflect.MessageDescriptor, templateID uint32) {
	name := string(md.Name())
	fmt.Fprintf(b, "    <sbe:message name=\"%s\" id=\"%d\">\n", name, templateID)

	sorted := sortedFields(md)
	for _, f := range sorted {
		if f.IsList() && f.Kind() == protoreflect.MessageKind {
			p2xWriteGroup(b, f, "        ")
			continue
		}
		p2xWriteField(b, f, "        ")
	}

	fmt.Fprintf(b, "    </sbe:message>\n")
}

func p2xWriteField(b *strings.Builder, fd protoreflect.FieldDescriptor, indent string) {
	fieldName := snakeToCamel(string(fd.Name()))
	fieldID := fd.Number()

	if fd.Kind() == protoreflect.EnumKind {
		enumName := string(fd.Enum().Name())
		fmt.Fprintf(b, "%s<field name=\"%s\" id=\"%d\" type=\"%s\"/>\n", indent, fieldName, fieldID, enumName)
		return
	}

	if fd.Kind() == protoreflect.MessageKind && !fd.IsList() {
		msgName := string(fd.Message().Name())
		fmt.Fprintf(b, "%s<field name=\"%s\" id=\"%d\" type=\"%s\"/>\n", indent, fieldName, fieldID, msgName)
		return
	}

	info := protoFieldToSBEType(fd)
	if info.length > 0 {
		fmt.Fprintf(b, "%s<field name=\"%s\" id=\"%d\" type=\"str%d\"/>\n", indent, fieldName, fieldID, info.length)
	} else {
		fmt.Fprintf(b, "%s<field name=\"%s\" id=\"%d\" type=\"%s\"/>\n", indent, fieldName, fieldID, info.xmlType)
	}
}

func p2xWriteGroup(b *strings.Builder, fd protoreflect.FieldDescriptor, indent string) {
	groupName := snakeToCamel(string(fd.Name()))
	groupID := fd.Number()

	fmt.Fprintf(b, "%s<group name=\"%s\" id=\"%d\">\n", indent, groupName, groupID)

	md := fd.Message()
	sorted := sortedFields(md)
	for _, f := range sorted {
		p2xWriteField(b, f, indent+"    ")
	}

	fmt.Fprintf(b, "%s</group>\n", indent)
}

type sbeTypeInfo struct {
	primitiveType string
	xmlType       string
	length        uint32
}

func protoFieldToSBEType(fd protoreflect.FieldDescriptor) sbeTypeInfo {
	if enc, ok := getFieldStringOption(fd, extEncoding); ok {
		return sbeTypeInfo{primitiveType: enc, xmlType: enc}
	}

	if fd.Kind() == protoreflect.StringKind || fd.Kind() == protoreflect.BytesKind {
		length, _ := getFieldUint32Option(fd, extLength)
		return sbeTypeInfo{primitiveType: "char", xmlType: "char", length: length}
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		return sbeTypeInfo{primitiveType: "uint8", xmlType: "uint8"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return sbeTypeInfo{primitiveType: "int32", xmlType: "int32"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return sbeTypeInfo{primitiveType: "int64", xmlType: "int64"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return sbeTypeInfo{primitiveType: "uint32", xmlType: "uint32"}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return sbeTypeInfo{primitiveType: "uint64", xmlType: "uint64"}
	case protoreflect.FloatKind:
		return sbeTypeInfo{primitiveType: "float", xmlType: "float"}
	case protoreflect.DoubleKind:
		return sbeTypeInfo{primitiveType: "double", xmlType: "double"}
	default:
		return sbeTypeInfo{primitiveType: "uint8", xmlType: "uint8"}
	}
}
