// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Package sbe provides SBE (Simple Binary Encoding) marshaling for protobuf
// messages.
//
// Messages are encoded following the FIX SBE standard. Proto descriptors with
// custom SBE annotations define the message layout:
//
//	option (sbe.schema_id) = 1;
//	option (sbe.version) = 0;
//
//	message Order {
//	    option (sbe.template_id) = 1;
//	    uint64 order_id = 1;
//	    string symbol = 2 [(sbe.length) = 8];
//	}
//
// A [Codec] is created from proto file descriptors and produces standard SBE
// binary compatible with any SBE implementation.
//
// Supported proto field types:
//   - Scalar integers (int32, int64, uint32, uint64, sint32, sint64, fixed*, sfixed*)
//   - bool, float, double, enum
//   - string and bytes (require [(sbe.length)] annotation for fixed size)
//   - Nested messages (encoded as SBE composites, inlined at fixed offsets)
//   - repeated messages (encoded as SBE repeating groups)
//
// Unsupported: map fields, oneof, repeated scalars.
//
// # Concurrency
//
// A [Codec] is safe for concurrent use after construction. Its
// internal template tables are read-only once [NewCodec] returns.
//
// A [View] returned by [Codec.View] is a value; copies are independent
// and safe to read from multiple goroutines, provided the underlying
// buffer is not mutated. View accessors read from that buffer
// directly — see the View documentation for the trust model.
//
// [Encoder] and [Decoder] (the SOFH-framed stream wrappers) hold a
// reusable scratch buffer and are not safe for concurrent use; create
// one per goroutine, or guard with a mutex.
package sbe

import (
	"encoding/binary"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/check"
)

const (
	// headerSize is the SBE message header size in bytes:
	// blockLength(2) + templateId(2) + schemaId(2) + version(2).
	headerSize = 8

	// groupHeaderSize is the SBE repeating group header size:
	// blockLength(2) + numInGroup(2).
	groupHeaderSize = 4
)

// SBE encoding type identifiers matching the specification.
const (
	encInt8   = "int8"
	encInt16  = "int16"
	encInt32  = "int32"
	encInt64  = "int64"
	encUint8  = "uint8"
	encUint16 = "uint16"
	encUint32 = "uint32"
	encUint64 = "uint64"
	encFloat  = "float"
	encDouble = "double"
	encChar   = "char"
)

// byteOrder is little-endian per the SBE specification.
var byteOrder = binary.LittleEndian

// Codec encodes and decodes proto.Message values using the SBE binary format.
// A Codec is safe for concurrent use after creation.
type Codec struct {
	byName    map[protoreflect.FullName]*messageTemplate
	byID      map[uint16]*messageTemplate
	validator check.Validator
}

// CodecOptions configures Codec construction. The zero value is
// equivalent to the package-level [NewCodec].
type CodecOptions struct {
	// Validator, if non-nil, runs data validation on every message
	// decoded by [Codec.Unmarshal], [Codec.UnmarshalDescriptor], and
	// [Decoder.Decode]. When it reports violations the decode fails
	// with a *check.Error, retrievable via errors.As. It does not
	// apply to [Codec.View], which reads the wire buffer without
	// materializing a message. The validator is fixed at construction
	// so the Codec stays safe for concurrent use.
	Validator check.Validator
}

// NewCodec creates an SBE codec from proto file descriptors.
// Each file must have (sbe.schema_id) and (sbe.version) file options, and
// each message to be encoded must have an (sbe.template_id) message option.
func NewCodec(files ...protoreflect.FileDescriptor) (*Codec, error) {
	return CodecOptions{}.NewCodec(files...)
}

// NewCodec creates an SBE codec from proto file descriptors with the
// given options.
func (o CodecOptions) NewCodec(files ...protoreflect.FileDescriptor) (*Codec, error) {
	c := &Codec{
		byName:    make(map[protoreflect.FullName]*messageTemplate),
		byID:      make(map[uint16]*messageTemplate),
		validator: o.Validator,
	}
	for _, fd := range files {
		schemaID, ok := getFileUint32Option(fd, extSchemaID)
		if !ok {
			return nil, fmt.Errorf("sbe: file %s missing (sbe.schema_id) option", fd.Path())
		}
		version, _ := getFileUint32Option(fd, extVersion)

		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			if err := c.registerMessage(msgs.Get(i), uint16(schemaID), uint16(version)); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

func (c *Codec) registerMessage(md protoreflect.MessageDescriptor, schemaID, version uint16) error {
	// Only register messages that have a template_id (top-level SBE messages).
	if _, ok := getMessageUint32Option(md, extTemplateID); ok {
		tmpl, err := buildTemplate(md, schemaID, version)
		if err != nil {
			return err
		}
		tmpl.view = buildViewSchema(tmpl)
		c.byName[md.FullName()] = tmpl
		c.byID[tmpl.templateID] = tmpl
	}

	// Recursively register nested messages.
	nested := md.Messages()
	for i := 0; i < nested.Len(); i++ {
		if err := c.registerMessage(nested.Get(i), schemaID, version); err != nil {
			return err
		}
	}
	return nil
}

// Marshal encodes msg to SBE binary format.
func (c *Codec) Marshal(msg proto.Message) ([]byte, error) {
	name := msg.ProtoReflect().Descriptor().FullName()
	tmpl, ok := c.byName[name]
	if !ok {
		return nil, fmt.Errorf("sbe: no template registered for %s", name)
	}
	return marshalMessage(msg.ProtoReflect(), tmpl)
}

// Unmarshal decodes SBE binary into msg.
func (c *Codec) Unmarshal(data []byte, msg proto.Message) error {
	name := msg.ProtoReflect().Descriptor().FullName()
	tmpl, ok := c.byName[name]
	if !ok {
		return fmt.Errorf("sbe: no template registered for %s", name)
	}
	if err := unmarshalMessage(data, msg.ProtoReflect(), tmpl); err != nil {
		return err
	}
	_, err := check.Validate(c.validator, msg)
	return err
}

// UnmarshalDescriptor decodes SBE binary into a new dynamicpb.Message.
func (c *Codec) UnmarshalDescriptor(data []byte, desc protoreflect.MessageDescriptor) (*dynamicpb.Message, error) {
	tmpl, ok := c.byName[desc.FullName()]
	if !ok {
		return nil, fmt.Errorf("sbe: no template registered for %s", desc.FullName())
	}
	msg := dynamicpb.NewMessage(desc)
	if err := unmarshalMessage(data, msg, tmpl); err != nil {
		return nil, err
	}
	if _, err := check.Validate(c.validator, msg); err != nil {
		return nil, err
	}
	return msg, nil
}
