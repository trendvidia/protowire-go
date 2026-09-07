// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/check"
)

// Unmarshal parses PXF data into msg with default options.
func Unmarshal(data []byte, msg proto.Message) error {
	return UnmarshalOptions{}.Unmarshal(data, msg)
}

// UnmarshalDescriptor parses PXF data using the given message descriptor.
func UnmarshalDescriptor(data []byte, desc protoreflect.MessageDescriptor) (*dynamicpb.Message, error) {
	return UnmarshalOptions{}.UnmarshalDescriptor(data, desc)
}

// UnmarshalFullDescriptor parses PXF data using the given message descriptor
// and returns field presence metadata. It validates required fields and applies defaults.
func UnmarshalFullDescriptor(data []byte, desc protoreflect.MessageDescriptor) (*dynamicpb.Message, *Result, error) {
	return UnmarshalOptions{}.UnmarshalFullDescriptor(data, desc)
}

// Unmarshal parses PXF data into msg.
func (o UnmarshalOptions) Unmarshal(data []byte, msg proto.Message) error {
	r := msg.ProtoReflect()
	if !o.SkipValidate {
		if err := asValidationError(ValidateFile(r.Descriptor().ParentFile())); err != nil {
			return err
		}
	}
	if err := unmarshalDirect(data, r, o); err != nil {
		return err
	}
	_, err := check.Validate(o.Validator, msg)
	return err
}

// UnmarshalDescriptor parses PXF data using the given message descriptor.
func (o UnmarshalOptions) UnmarshalDescriptor(data []byte, desc protoreflect.MessageDescriptor) (*dynamicpb.Message, error) {
	if !o.SkipValidate {
		if err := asValidationError(ValidateDescriptor(desc)); err != nil {
			return nil, err
		}
	}
	msg := dynamicpb.NewMessage(desc)
	if err := unmarshalDirect(data, msg.ProtoReflect(), o); err != nil {
		return nil, err
	}
	if _, err := check.Validate(o.Validator, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// UnmarshalFullDescriptor parses PXF data using the given message descriptor
// and returns field presence metadata. If a Validator reports violations,
// the decoded message and the Result (with the check.Report attached) are
// returned alongside the *check.Error.
func (o UnmarshalOptions) UnmarshalFullDescriptor(data []byte, desc protoreflect.MessageDescriptor) (*dynamicpb.Message, *Result, error) {
	if !o.SkipValidate {
		if err := asValidationError(ValidateDescriptor(desc)); err != nil {
			return nil, nil, err
		}
	}
	msg := dynamicpb.NewMessage(desc)
	result, err := unmarshalDirectFull(data, msg.ProtoReflect(), o)
	if err != nil {
		return nil, nil, err
	}
	result.report, err = check.Validate(o.Validator, msg)
	return msg, result, err
}

// decodeMapKey parses one map key. field is the map field itself (its
// MapKey() is the key's descriptor); quoted reports whether the key was
// written as a string literal, which decides what it may spell for a
// bool key.
func decodeMapKey(field protoreflect.FieldDescriptor, key string, quoted bool, pos Position) (protoreflect.MapKey, error) {
	fd := field.MapKey()
	switch fd.Kind() {
	case protoreflect.StringKind:
		if !utf8.ValidString(key) {
			return protoreflect.MapKey{}, errorf(pos, "invalid UTF-8 in string map key")
		}
		return protoreflect.ValueOfString(key).MapKey(), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(key, 10, 32)
		if err != nil {
			return protoreflect.MapKey{}, errorf(pos, "invalid int32 map key: %s", key)
		}
		return protoreflect.ValueOfInt32(int32(n)).MapKey(), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return protoreflect.MapKey{}, errorf(pos, "invalid int64 map key: %s", key)
		}
		return protoreflect.ValueOfInt64(n).MapKey(), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(key, 10, 32)
		if err != nil {
			return protoreflect.MapKey{}, errorf(pos, "invalid uint32 map key: %s", key)
		}
		return protoreflect.ValueOfUint32(uint32(n)).MapKey(), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			return protoreflect.MapKey{}, errorf(pos, "invalid uint64 map key: %s", key)
		}
		return protoreflect.ValueOfUint64(n).MapKey(), nil
	case protoreflect.BoolKind:
		// A bool map key has two spellings in the grammar: the bare
		// integers 0 / 1 (draft -01 §entries-and-keys: an integer key
		// matches "bool encoded as 0/1"), and the quoted literals "true"
		// / "false" (a string key "is parsed as a literal of K's type",
		// and a PXF bool literal is exactly those two words, #90). Bare
		// true / false never reach here: the lexer emits BOOL for them
		// and map-key is identifier / string / integer, so the caller
		// reports the production error.
		//
		// NOT strconv.ParseBool, which also takes t, T, TRUE, True, f, F,
		// FALSE, False — eight spellings no port that follows the grammar
		// binds, so a document using one bound here and was a syntax
		// error everywhere else (#93). "1" / "0" in quotes are rejected
		// too: an integer literal inside a string is not a bool literal
		// (decided in #93). Whether a bare true / false should be a key
		// at all is open as protowire#284.
		if quoted {
			switch key {
			case "true":
				return protoreflect.ValueOfBool(true).MapKey(), nil
			case "false":
				return protoreflect.ValueOfBool(false).MapKey(), nil
			}
			return protoreflect.MapKey{}, errorf(pos, "invalid bool map key %q for field %q: a bool key is 0, 1, \"true\" or \"false\"", key, field.Name())
		}
		switch key {
		case "1":
			return protoreflect.ValueOfBool(true).MapKey(), nil
		case "0":
			return protoreflect.ValueOfBool(false).MapKey(), nil
		}
		return protoreflect.MapKey{}, errorf(pos, "invalid bool map key %s for field %q: a bool key is 0, 1, \"true\" or \"false\"", key, field.Name())
	default:
		return protoreflect.MapKey{}, errorf(pos, "unsupported map key kind: %s", fd.Kind())
	}
}
