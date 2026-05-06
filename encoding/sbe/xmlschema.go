// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"encoding/xml"
	"fmt"
	"strings"
	"unicode"
)

// XMLSchema represents an SBE XML message schema.
type XMLSchema struct {
	XMLName     xml.Name     `xml:"messageSchema"`
	Package     string       `xml:"package,attr"`
	ID          uint32       `xml:"id,attr"`
	Version     uint32       `xml:"version,attr"`
	ByteOrder   string       `xml:"byteOrder,attr"`
	Description string       `xml:"description,attr"`
	Types       XMLTypes     `xml:"types"`
	Messages    []XMLMessage `xml:"message"`
}

// XMLTypes holds the type definitions within an SBE schema.
type XMLTypes struct {
	Types      []XMLType      `xml:"type"`
	Composites []XMLComposite `xml:"composite"`
	Enums      []XMLEnum      `xml:"enum"`
}

// XMLType represents an SBE simple type definition.
type XMLType struct {
	Name          string `xml:"name,attr"`
	PrimitiveType string `xml:"primitiveType,attr"`
	Length        uint32 `xml:"length,attr,omitempty"`
	Description   string `xml:"description,attr,omitempty"`
}

// XMLComposite represents an SBE composite type definition.
type XMLComposite struct {
	Name        string    `xml:"name,attr"`
	Description string    `xml:"description,attr,omitempty"`
	Types       []XMLType `xml:"type"`
	Refs        []XMLRef  `xml:"ref"`
}

// XMLRef represents a reference to another type within a composite.
type XMLRef struct {
	Name string `xml:"name,attr"`
	Type string `xml:"type,attr"`
}

// XMLEnum represents an SBE enum type definition.
type XMLEnum struct {
	Name         string          `xml:"name,attr"`
	EncodingType string          `xml:"encodingType,attr"`
	Description  string          `xml:"description,attr,omitempty"`
	ValidValues  []XMLValidValue `xml:"validValue"`
}

// XMLValidValue represents one value in an SBE enum.
type XMLValidValue struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

// XMLMessage represents an SBE message (template).
type XMLMessage struct {
	Name        string     `xml:"name,attr"`
	ID          uint32     `xml:"id,attr"`
	Description string     `xml:"description,attr,omitempty"`
	Fields      []XMLField `xml:"field"`
	Groups      []XMLGroup `xml:"group"`
}

// XMLField represents a field within an SBE message or group.
type XMLField struct {
	Name string `xml:"name,attr"`
	ID   uint32 `xml:"id,attr"`
	Type string `xml:"type,attr"`
}

// XMLGroup represents an SBE repeating group.
type XMLGroup struct {
	Name   string     `xml:"name,attr"`
	ID     uint32     `xml:"id,attr"`
	Fields []XMLField `xml:"field"`
}

// ParseXMLSchema parses an SBE XML schema.
func ParseXMLSchema(data []byte) (*XMLSchema, error) {
	// Strip SBE namespace prefix for encoding/xml compatibility.
	input := stripSBENamespace(data)
	var schema XMLSchema
	if err := xml.Unmarshal(input, &schema); err != nil {
		return nil, fmt.Errorf("sbe: parse XML schema: %w", err)
	}
	return &schema, nil
}

func stripSBENamespace(data []byte) []byte {
	s := string(data)
	s = strings.ReplaceAll(s, "sbe:message", "message")
	return []byte(s)
}

// Name conversion helpers used by both XML→proto and proto→XML converters.

func camelToSnake(s string) string {
	var buf strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				if unicode.IsLower(prev) {
					buf.WriteByte('_')
				} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					buf.WriteByte('_')
				}
			}
			buf.WriteRune(unicode.ToLower(r))
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	var buf strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == 0 {
			buf.WriteString(p)
		} else {
			r := []rune(p)
			r[0] = unicode.ToUpper(r[0])
			buf.WriteString(string(r))
		}
	}
	return buf.String()
}

func camelToScreamingSnake(s string) string {
	var buf strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && unicode.IsLower(runes[i-1]) {
			buf.WriteByte('_')
		}
		buf.WriteRune(unicode.ToUpper(r))
	}
	return buf.String()
}

func screamingSnakeToPascal(s string) string {
	parts := strings.Split(strings.ToLower(s), "_")
	var buf strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		r[0] = unicode.ToUpper(r[0])
		buf.WriteString(string(r))
	}
	return buf.String()
}

func stripEnumPrefix(valueName, enumName string) string {
	prefix := camelToScreamingSnake(enumName) + "_"
	if strings.HasPrefix(valueName, prefix) {
		return screamingSnakeToPascal(valueName[len(prefix):])
	}
	return screamingSnakeToPascal(valueName)
}

func singularPascal(s string) string {
	if s == "" {
		return s
	}
	if strings.HasSuffix(s, "ies") && len(s) > 3 {
		s = s[:len(s)-3] + "y"
	} else if strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 1 {
		s = s[:len(s)-1]
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
