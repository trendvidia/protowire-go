// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// messageTemplate describes the SBE wire layout for a proto message.
type messageTemplate struct {
	templateID  uint16
	schemaID    uint16
	version     uint16
	blockLength uint16
	fields      []fieldTemplate
	groups      []groupTemplate
	view        *viewSchema // pre-computed lookup for View access
}

// fieldTemplate describes one field's position in the SBE block.
type fieldTemplate struct {
	fd            protoreflect.FieldDescriptor
	offset        uint16
	size          uint16
	encoding      string          // SBE primitive type; empty for composites
	composite     []fieldTemplate // non-nil for composite (nested message) fields
	compositeView *viewSchema     // pre-computed lookup for View access into composites
}

// groupTemplate describes an SBE repeating group.
type groupTemplate struct {
	fd          protoreflect.FieldDescriptor
	blockLength uint16
	fields      []fieldTemplate
}

func buildTemplate(md protoreflect.MessageDescriptor, schemaID, version uint16) (*messageTemplate, error) {
	tid, ok := getMessageUint32Option(md, extTemplateID)
	if !ok {
		return nil, fmt.Errorf("sbe: message %s missing (sbe.template_id)", md.FullName())
	}

	tmpl := &messageTemplate{
		templateID: uint16(tid),
		schemaID:   schemaID,
		version:    version,
	}

	sorted := sortedFields(md)

	var offset uint16
	for _, fd := range sorted {
		if fd.IsMap() {
			return nil, fmt.Errorf("sbe: map field %s.%s not supported", md.FullName(), fd.Name())
		}
		if fd.ContainingOneof() != nil && !fd.ContainingOneof().IsSynthetic() {
			return nil, fmt.Errorf("sbe: oneof field %s.%s not supported", md.FullName(), fd.Name())
		}

		// Repeated message → SBE group (after root block).
		if fd.IsList() && fd.Kind() == protoreflect.MessageKind {
			gt, err := buildGroupTemplate(fd)
			if err != nil {
				return nil, err
			}
			tmpl.groups = append(tmpl.groups, gt)
			continue
		}

		// Repeated scalar → not supported.
		if fd.IsList() {
			return nil, fmt.Errorf("sbe: repeated scalar field %s.%s not supported; wrap in a message", md.FullName(), fd.Name())
		}

		// Non-repeated message → composite (inlined).
		if fd.Kind() == protoreflect.MessageKind {
			size, subFields, err := buildCompositeFields(fd.Message())
			if err != nil {
				return nil, fmt.Errorf("sbe: composite %s.%s: %w", md.FullName(), fd.Name(), err)
			}
			tmpl.fields = append(tmpl.fields, fieldTemplate{
				fd:        fd,
				offset:    offset,
				size:      size,
				composite: subFields,
			})
			offset += size
			continue
		}

		enc, size, err := fieldEncodingSize(fd)
		if err != nil {
			return nil, fmt.Errorf("sbe: field %s.%s: %w", md.FullName(), fd.Name(), err)
		}
		tmpl.fields = append(tmpl.fields, fieldTemplate{
			fd:       fd,
			offset:   offset,
			size:     size,
			encoding: enc,
		})
		offset += size
	}

	tmpl.blockLength = offset
	return tmpl, nil
}

func buildGroupTemplate(fd protoreflect.FieldDescriptor) (groupTemplate, error) {
	md := fd.Message()
	sorted := sortedFields(md)

	gt := groupTemplate{fd: fd}
	var offset uint16
	for _, f := range sorted {
		if f.IsMap() {
			return gt, fmt.Errorf("sbe: map field in group %s not supported", md.FullName())
		}
		if f.IsList() {
			return gt, fmt.Errorf("sbe: nested repeated field in group %s not supported", md.FullName())
		}

		if f.Kind() == protoreflect.MessageKind {
			size, subFields, err := buildCompositeFields(f.Message())
			if err != nil {
				return gt, fmt.Errorf("sbe: composite in group %s.%s: %w", md.FullName(), f.Name(), err)
			}
			gt.fields = append(gt.fields, fieldTemplate{
				fd:        f,
				offset:    offset,
				size:      size,
				composite: subFields,
			})
			offset += size
			continue
		}

		enc, size, err := fieldEncodingSize(f)
		if err != nil {
			return gt, fmt.Errorf("sbe: group field %s.%s: %w", md.FullName(), f.Name(), err)
		}
		gt.fields = append(gt.fields, fieldTemplate{
			fd:       f,
			offset:   offset,
			size:     size,
			encoding: enc,
		})
		offset += size
	}
	gt.blockLength = offset
	return gt, nil
}

func buildCompositeFields(md protoreflect.MessageDescriptor) (uint16, []fieldTemplate, error) {
	sorted := sortedFields(md)

	var fts []fieldTemplate
	var offset uint16
	for _, fd := range sorted {
		if fd.IsList() || fd.IsMap() {
			return 0, nil, fmt.Errorf("composite %s contains list/map field %s", md.FullName(), fd.Name())
		}
		if fd.ContainingOneof() != nil && !fd.ContainingOneof().IsSynthetic() {
			return 0, nil, fmt.Errorf("composite %s contains oneof field %s", md.FullName(), fd.Name())
		}

		if fd.Kind() == protoreflect.MessageKind {
			size, subFields, err := buildCompositeFields(fd.Message())
			if err != nil {
				return 0, nil, err
			}
			fts = append(fts, fieldTemplate{
				fd:        fd,
				offset:    offset,
				size:      size,
				composite: subFields,
			})
			offset += size
			continue
		}

		enc, size, err := fieldEncodingSize(fd)
		if err != nil {
			return 0, nil, fmt.Errorf("composite field %s.%s: %w", md.FullName(), fd.Name(), err)
		}
		fts = append(fts, fieldTemplate{
			fd:       fd,
			offset:   offset,
			size:     size,
			encoding: enc,
		})
		offset += size
	}
	return offset, fts, nil
}

// fieldEncodingSize returns the SBE encoding name and byte size for a proto field.
func fieldEncodingSize(fd protoreflect.FieldDescriptor) (string, uint16, error) {
	// Explicit encoding override via (sbe.encoding).
	if enc, ok := getFieldStringOption(fd, extEncoding); ok {
		switch enc {
		case encInt8, encUint8:
			return enc, 1, nil
		case encInt16, encUint16:
			return enc, 2, nil
		case encInt32, encUint32, encFloat:
			return enc, 4, nil
		case encInt64, encUint64, encDouble:
			return enc, 8, nil
		default:
			return "", 0, fmt.Errorf("unknown encoding %q", enc)
		}
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		return encUint8, 1, nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return encInt32, 4, nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return encInt64, 8, nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return encUint32, 4, nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return encUint64, 8, nil
	case protoreflect.FloatKind:
		return encFloat, 4, nil
	case protoreflect.DoubleKind:
		return encDouble, 8, nil
	case protoreflect.EnumKind:
		return encUint8, 1, nil
	case protoreflect.StringKind:
		length, ok := getFieldUint32Option(fd, extLength)
		if !ok {
			return "", 0, fmt.Errorf("string field requires (sbe.length) annotation")
		}
		return encChar, uint16(length), nil
	case protoreflect.BytesKind:
		length, ok := getFieldUint32Option(fd, extLength)
		if !ok {
			return "", 0, fmt.Errorf("bytes field requires (sbe.length) annotation")
		}
		return encChar, uint16(length), nil
	default:
		return "", 0, fmt.Errorf("unsupported proto kind %s", fd.Kind())
	}
}

// sortedFields returns message fields sorted by field number (SBE wire order).
func sortedFields(md protoreflect.MessageDescriptor) []protoreflect.FieldDescriptor {
	fields := md.Fields()
	sorted := make([]protoreflect.FieldDescriptor, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		sorted = append(sorted, fields.Get(i))
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Number() < sorted[j].Number()
	})
	return sorted
}
