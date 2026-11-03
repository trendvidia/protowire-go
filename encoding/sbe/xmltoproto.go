// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"strings"
	"unicode"
)

// XMLToProto converts an SBE XML schema to a .proto file with SBE annotations.
func XMLToProto(xmlData []byte) ([]byte, error) {
	schema, err := ParseXMLSchema(xmlData)
	if err != nil {
		return nil, err
	}
	return generateProto(schema)
}

func generateProto(schema *XMLSchema) ([]byte, error) {
	// Build type resolution maps.
	typeMap := make(map[string]*XMLType)
	compositeMap := make(map[string]*XMLComposite)
	enumMap := make(map[string]*XMLEnum)

	// Pre-populate with built-in SBE primitives.
	for _, name := range []string{"int8", "int16", "int32", "int64",
		"uint8", "uint16", "uint32", "uint64", "float", "double", "char"} {
		typeMap[name] = &XMLType{Name: name, PrimitiveType: name}
	}

	for i := range schema.Types.Types {
		t := &schema.Types.Types[i]
		typeMap[t.Name] = t
	}
	for i := range schema.Types.Composites {
		c := &schema.Types.Composites[i]
		compositeMap[c.Name] = c
	}
	for i := range schema.Types.Enums {
		e := &schema.Types.Enums[i]
		enumMap[e.Name] = e
	}

	var b strings.Builder

	b.WriteString("syntax = \"proto3\";\n\n")
	if schema.Package != "" {
		fmt.Fprintf(&b, "package %s;\n\n", schema.Package)
	}
	b.WriteString("import \"sbe/annotations.proto\";\n\n")
	fmt.Fprintf(&b, "option (sbe.schema_id) = %d;\n", schema.ID)
	fmt.Fprintf(&b, "option (sbe.version) = %d;\n\n", schema.Version)

	// Enums.
	for i := range schema.Types.Enums {
		writeProtoEnum(&b, &schema.Types.Enums[i], "")
	}

	// Composites as messages (skip standard SBE infrastructure types).
	for i := range schema.Types.Composites {
		c := &schema.Types.Composites[i]
		if c.Name == "messageHeader" || c.Name == "groupSizeEncoding" {
			continue
		}
		if err := writeProtoComposite(&b, c); err != nil {
			return nil, err
		}
	}

	// Messages.
	for i := range schema.Messages {
		if err := writeProtoMessage(&b, &schema.Messages[i], typeMap, compositeMap, enumMap, ""); err != nil {
			return nil, err
		}
	}

	return []byte(b.String()), nil
}

func writeProtoEnum(b *strings.Builder, e *XMLEnum, indent string) {
	fmt.Fprintf(b, "%senum %s {\n", indent, e.Name)
	prefix := camelToScreamingSnake(e.Name)
	for _, v := range e.ValidValues {
		name := prefix + "_" + camelToScreamingSnake(v.Name)
		fmt.Fprintf(b, "%s  %s = %s;\n", indent, name, v.Value)
	}
	fmt.Fprintf(b, "%s}\n\n", indent)
}

func writeProtoComposite(b *strings.Builder, c *XMLComposite) error {
	fmt.Fprintf(b, "message %s {\n", c.Name)
	fieldNum := 1
	for _, t := range c.Types {
		protoType, opts, note, err := resolveTypeToProto(&t)
		if err != nil {
			return fmt.Errorf("sbe: composite %q: %w", c.Name, err)
		}
		name := camelToSnake(t.Name)
		writeNote(b, "  ", note)
		if opts != "" {
			fmt.Fprintf(b, "  %s %s = %d [%s];\n", protoType, name, fieldNum, opts)
		} else {
			fmt.Fprintf(b, "  %s %s = %d;\n", protoType, name, fieldNum)
		}
		fieldNum++
	}
	for _, r := range c.Refs {
		name := camelToSnake(r.Name)
		fmt.Fprintf(b, "  %s %s = %d;\n", r.Type, name, fieldNum)
		fieldNum++
	}
	fmt.Fprintf(b, "}\n\n")
	return nil
}

func writeProtoMessage(b *strings.Builder, msg *XMLMessage, typeMap map[string]*XMLType, compositeMap map[string]*XMLComposite, enumMap map[string]*XMLEnum, indent string) error {
	fmt.Fprintf(b, "%smessage %s {\n", indent, msg.Name)
	fmt.Fprintf(b, "%s  option (sbe.template_id) = %d;\n", indent, msg.ID)

	for i := range msg.Fields {
		if err := writeProtoField(b, &msg.Fields[i], typeMap, compositeMap, enumMap, indent+"  "); err != nil {
			return fmt.Errorf("sbe: message %q: %w", msg.Name, err)
		}
	}
	for i := range msg.Groups {
		if err := writeProtoGroup(b, &msg.Groups[i], typeMap, compositeMap, enumMap, indent+"  "); err != nil {
			return fmt.Errorf("sbe: message %q: %w", msg.Name, err)
		}
	}

	fmt.Fprintf(b, "%s}\n\n", indent)
	return nil
}

func writeProtoField(b *strings.Builder, f *XMLField, typeMap map[string]*XMLType, compositeMap map[string]*XMLComposite, enumMap map[string]*XMLEnum, indent string) error {
	name := camelToSnake(f.Name)

	// Enum reference.
	if _, ok := enumMap[f.Type]; ok {
		fmt.Fprintf(b, "%s%s %s = %d;\n", indent, f.Type, name, f.ID)
		return nil
	}

	// Composite reference.
	if _, ok := compositeMap[f.Type]; ok {
		fmt.Fprintf(b, "%s%s %s = %d;\n", indent, f.Type, name, f.ID)
		return nil
	}

	// Resolve from type map (built-in or user-defined).
	if t, ok := typeMap[f.Type]; ok {
		protoType, opts, note, err := resolveTypeToProto(t)
		if err != nil {
			return fmt.Errorf("field %q: %w", f.Name, err)
		}
		writeNote(b, indent, note)
		if opts != "" {
			fmt.Fprintf(b, "%s%s %s = %d [%s];\n", indent, protoType, name, f.ID, opts)
		} else {
			fmt.Fprintf(b, "%s%s %s = %d;\n", indent, protoType, name, f.ID)
		}
		return nil
	}

	// Unknown type — pass through as-is.
	fmt.Fprintf(b, "%s%s %s = %d;\n", indent, f.Type, name, f.ID)
	return nil
}

func writeProtoGroup(b *strings.Builder, g *XMLGroup, typeMap map[string]*XMLType, compositeMap map[string]*XMLComposite, enumMap map[string]*XMLEnum, indent string) error {
	msgName := singularPascal(g.Name)

	fmt.Fprintf(b, "%smessage %s {\n", indent, msgName)
	for i := range g.Fields {
		if err := writeProtoField(b, &g.Fields[i], typeMap, compositeMap, enumMap, indent+"  "); err != nil {
			return fmt.Errorf("group %q: %w", g.Name, err)
		}
	}
	fmt.Fprintf(b, "%s}\n", indent)

	fieldName := camelToSnake(g.Name)
	fmt.Fprintf(b, "%srepeated %s %s = %d;\n", indent, msgName, fieldName, g.ID)
	return nil
}

// resolveTypeToProto maps an SBE type to a proto type and optional field
// options. note, when not empty, is a comment for the line above the field.
// It refuses an array that no proto field can hold, rather than importing it
// as one element and misplacing every later field in the block.
func resolveTypeToProto(t *XMLType) (protoType, opts, note string, err error) {
	if t.PrimitiveType == "char" {
		length := t.Length
		if length == 0 {
			length = 1
		}
		opts = fmt.Sprintf("(sbe.length) = %d", length)
		if !isUTF8Compatible(t.CharacterEncoding) {
			// A proto string holds UTF-8 only, and a decoder refuses a
			// string that is not, so text in another encoding is carried as
			// bytes rather than failing at run time (protowire#494).
			return "bytes", opts, fmt.Sprintf("SBE characterEncoding %q is not UTF-8 compatible, so this field is bytes.", t.CharacterEncoding), nil
		}
		return "string", opts, "", nil
	}
	if t.Length > 1 {
		switch t.PrimitiveType {
		case "uint8", "int8":
			// A fixed-length array of octets is raw bytes, the same N bytes
			// on the wire as a bytes field with (sbe.length) = N.
			return "bytes", fmt.Sprintf("(sbe.length) = %d", t.Length), "", nil
		}
		return "", "", "", fmt.Errorf("type %q is an array of %d %s, which no proto field represents", t.Name, t.Length, t.PrimitiveType)
	}
	protoType, opts = resolvePrimitiveToProto(t.PrimitiveType)
	return protoType, opts, "", nil
}

// writeNote writes note as a comment line, if there is one.
func writeNote(b *strings.Builder, indent, note string) {
	if note != "" {
		fmt.Fprintf(b, "%s// %s\n", indent, note)
	}
}

// isUTF8Compatible reports whether every string in a char array of the
// named encoding is valid UTF-8: none named (FIX SBE's default is US-ASCII),
// a US-ASCII name, or a UTF-8 name. Names are IANA charset names and their
// registered aliases, compared ignoring case and punctuation.
func isUTF8Compatible(encoding string) bool {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ':', ' ':
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(encoding))
	switch name {
	case "",
		// US-ASCII and its IANA aliases.
		"usascii", "ascii", "ansix341968", "ansix341986", "iso646us",
		"isoir6", "iso646irv1991", "us", "ibm367", "cp367", "csascii",
		// UTF-8.
		"utf8", "csutf8":
		return true
	}
	return false
}

// resolvePrimitiveToProto maps an SBE primitive type other than char.
func resolvePrimitiveToProto(primitiveType string) (string, string) {
	switch primitiveType {
	case "int8":
		return "int32", `(sbe.encoding) = "int8"`
	case "int16":
		return "int32", `(sbe.encoding) = "int16"`
	case "int32":
		return "int32", ""
	case "int64":
		return "int64", ""
	case "uint8":
		return "uint32", `(sbe.encoding) = "uint8"`
	case "uint16":
		return "uint32", `(sbe.encoding) = "uint16"`
	case "uint32":
		return "uint32", ""
	case "uint64":
		return "uint64", ""
	case "float":
		return "float", ""
	case "double":
		return "double", ""
	default:
		return primitiveType, ""
	}
}
