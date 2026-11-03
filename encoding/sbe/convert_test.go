// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

const testSBEXML = `<?xml version="1.0" encoding="UTF-8"?>
<sbe:messageSchema xmlns:sbe="http://fixprotocol.io/2016/sbe"
                   package="test.v1"
                   id="1"
                   version="0"
                   byteOrder="littleEndian">
    <types>
        <composite name="messageHeader">
            <type name="blockLength" primitiveType="uint16"/>
            <type name="templateId" primitiveType="uint16"/>
            <type name="schemaId" primitiveType="uint16"/>
            <type name="version" primitiveType="uint16"/>
        </composite>
        <composite name="groupSizeEncoding">
            <type name="blockLength" primitiveType="uint16"/>
            <type name="numInGroup" primitiveType="uint16"/>
        </composite>
        <enum name="Side" encodingType="uint8">
            <validValue name="Buy">0</validValue>
            <validValue name="Sell">1</validValue>
        </enum>
        <type name="str8" primitiveType="char" length="8"/>
        <composite name="Inner">
            <type name="x" primitiveType="int64"/>
            <type name="y" primitiveType="int64"/>
        </composite>
    </types>
    <sbe:message name="Order" id="1">
        <field name="orderId" id="1" type="uint64"/>
        <field name="symbol" id="2" type="str8"/>
        <field name="price" id="3" type="int64"/>
        <field name="quantity" id="4" type="uint32"/>
        <field name="side" id="5" type="Side"/>
        <field name="active" id="6" type="uint8"/>
        <field name="weight" id="7" type="double"/>
        <field name="score" id="8" type="float"/>
        <group name="fills" id="9">
            <field name="fillPrice" id="1" type="int64"/>
            <field name="fillQty" id="2" type="uint32"/>
            <field name="fillId" id="3" type="uint64"/>
        </group>
    </sbe:message>
    <sbe:message name="Simple" id="2">
        <field name="id" id="1" type="uint32"/>
        <field name="value" id="2" type="int32"/>
    </sbe:message>
    <sbe:message name="WithComposite" id="3">
        <field name="id" id="1" type="uint64"/>
        <field name="inner" id="2" type="Inner"/>
        <field name="code" id="3" type="int32"/>
    </sbe:message>
    <sbe:message name="WithNarrow" id="4">
        <field name="status" id="1" type="uint8"/>
        <field name="port" id="2" type="uint16"/>
        <field name="delta" id="3" type="int16"/>
    </sbe:message>
</sbe:messageSchema>`

func compileProtoSource(t *testing.T, src string) protoreflect.FileDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"sbe/annotations.proto": sbeAnnotationsSrc,
					"test.proto":            src,
				}),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	for _, f := range result {
		if f.Path() == "test.proto" {
			return f
		}
	}
	t.Fatal("test.proto not found")
	return nil
}

func TestParseXMLSchema(t *testing.T) {
	schema, err := sbe.ParseXMLSchema([]byte(testSBEXML))
	require.NoError(t, err)

	require.Equal(t, "test.v1", schema.Package)
	require.Equal(t, uint32(1), schema.ID)
	require.Equal(t, uint32(0), schema.Version)
	require.Len(t, schema.Types.Enums, 1)
	require.Equal(t, "Side", schema.Types.Enums[0].Name)
	require.Len(t, schema.Messages, 4)
}

func TestXMLToProto(t *testing.T) {
	protoBytes, err := sbe.XMLToProto([]byte(testSBEXML))
	require.NoError(t, err)

	proto := string(protoBytes)

	// Verify key elements are present.
	require.Contains(t, proto, `option (sbe.schema_id) = 1;`)
	require.Contains(t, proto, `option (sbe.version) = 0;`)
	require.Contains(t, proto, `option (sbe.template_id) = 1;`)
	require.Contains(t, proto, `string symbol = 2 [(sbe.length) = 8];`)
	require.Contains(t, proto, `Side side = 5;`)
	require.Contains(t, proto, `Inner inner = 2;`)
	require.Contains(t, proto, `repeated Fill fills = 9;`)
	require.Contains(t, proto, `(sbe.encoding) = "uint8"`)

	// Verify the generated proto compiles.
	fd := compileProtoSource(t, proto)

	// Build codec from compiled proto — validates annotations.
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	// Test Simple message round-trip through codec.
	desc := fd.Messages().ByName("Simple")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint32(42))
	msg.Set(desc.Fields().ByName("value"), protoreflect.ValueOfInt32(-100))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))
	require.Equal(t, uint64(42), got.Get(desc.Fields().ByName("id")).Uint())
	require.Equal(t, int64(-100), got.Get(desc.Fields().ByName("value")).Int())
}

func TestXMLToProtoComposite(t *testing.T) {
	protoBytes, err := sbe.XMLToProto([]byte(testSBEXML))
	require.NoError(t, err)

	fd := compileProtoSource(t, string(protoBytes))
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("WithComposite")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint64(99))

	innerField := desc.Fields().ByName("inner")
	innerDesc := innerField.Message()
	innerMsg := msg.Mutable(innerField).Message()
	innerMsg.Set(innerDesc.Fields().ByName("x"), protoreflect.ValueOfInt64(100))
	innerMsg.Set(innerDesc.Fields().ByName("y"), protoreflect.ValueOfInt64(-200))

	msg.Set(desc.Fields().ByName("code"), protoreflect.ValueOfInt32(42))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))
	require.Equal(t, uint64(99), got.Get(desc.Fields().ByName("id")).Uint())
	gotInner := got.Get(innerField).Message()
	require.Equal(t, int64(100), gotInner.Get(innerDesc.Fields().ByName("x")).Int())
	require.Equal(t, int64(-200), gotInner.Get(innerDesc.Fields().ByName("y")).Int())
	require.Equal(t, int64(42), got.Get(desc.Fields().ByName("code")).Int())
}

func TestXMLToProtoNarrow(t *testing.T) {
	protoBytes, err := sbe.XMLToProto([]byte(testSBEXML))
	require.NoError(t, err)

	fd := compileProtoSource(t, string(protoBytes))
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("WithNarrow")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("status"), protoreflect.ValueOfUint32(200))
	msg.Set(desc.Fields().ByName("port"), protoreflect.ValueOfUint32(8080))
	msg.Set(desc.Fields().ByName("delta"), protoreflect.ValueOfInt32(-1234))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)
	// Header(8) + uint8(1) + uint16(2) + int16(2) = 13
	require.Equal(t, 13, len(data))

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))
	require.Equal(t, uint64(200), got.Get(desc.Fields().ByName("status")).Uint())
	require.Equal(t, uint64(8080), got.Get(desc.Fields().ByName("port")).Uint())
	require.Equal(t, int64(-1234), got.Get(desc.Fields().ByName("delta")).Int())
}

func TestXMLRoundTrip(t *testing.T) {
	// XML → proto → compile → codec → marshal/unmarshal Order with fills.
	protoBytes, err := sbe.XMLToProto([]byte(testSBEXML))
	require.NoError(t, err)

	fd := compileProtoSource(t, string(protoBytes))
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("order_id"), protoreflect.ValueOfUint64(1001))
	msg.Set(desc.Fields().ByName("symbol"), protoreflect.ValueOfString("AAPL"))
	msg.Set(desc.Fields().ByName("price"), protoreflect.ValueOfInt64(19150))
	msg.Set(desc.Fields().ByName("quantity"), protoreflect.ValueOfUint32(100))
	msg.Set(desc.Fields().ByName("side"), protoreflect.ValueOfEnum(1))
	// active is uint32 [(sbe.encoding) = "uint8"] after XML→proto conversion.
	msg.Set(desc.Fields().ByName("active"), protoreflect.ValueOfUint32(1))
	msg.Set(desc.Fields().ByName("weight"), protoreflect.ValueOfFloat64(0.85))
	msg.Set(desc.Fields().ByName("score"), protoreflect.ValueOfFloat32(3.14))

	fillsField := desc.Fields().ByName("fills")
	fillDesc := fillsField.Message()
	list := msg.Mutable(fillsField).List()

	fill := list.NewElement()
	fill.Message().Set(fillDesc.Fields().ByName("fill_price"), protoreflect.ValueOfInt64(19155))
	fill.Message().Set(fillDesc.Fields().ByName("fill_qty"), protoreflect.ValueOfUint32(25))
	fill.Message().Set(fillDesc.Fields().ByName("fill_id"), protoreflect.ValueOfUint64(5001))
	list.Append(fill)

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))

	require.Equal(t, uint64(1001), got.Get(desc.Fields().ByName("order_id")).Uint())
	require.Equal(t, "AAPL", got.Get(desc.Fields().ByName("symbol")).String())
	require.Equal(t, int64(19150), got.Get(desc.Fields().ByName("price")).Int())
	require.Equal(t, uint64(100), got.Get(desc.Fields().ByName("quantity")).Uint())
	require.Equal(t, protoreflect.EnumNumber(1), got.Get(desc.Fields().ByName("side")).Enum())
	require.InDelta(t, 0.85, got.Get(desc.Fields().ByName("weight")).Float(), 1e-10)

	gotFills := got.Get(fillsField).List()
	require.Equal(t, 1, gotFills.Len())
	gotFill := gotFills.Get(0).Message()
	require.Equal(t, int64(19155), gotFill.Get(fillDesc.Fields().ByName("fill_price")).Int())
	require.Equal(t, uint64(25), gotFill.Get(fillDesc.Fields().ByName("fill_qty")).Uint())
	require.Equal(t, uint64(5001), gotFill.Get(fillDesc.Fields().ByName("fill_id")).Uint())
}

func TestProtoToXML(t *testing.T) {
	fd := compileTestProto(t)

	xmlBytes, err := sbe.ProtoToXML(fd)
	require.NoError(t, err)

	xmlStr := string(xmlBytes)

	// Verify XML structure.
	require.Contains(t, xmlStr, `package="test.v1"`)
	require.Contains(t, xmlStr, `id="1"`)
	require.Contains(t, xmlStr, `<sbe:message name="Order" id="1">`)
	require.Contains(t, xmlStr, `<sbe:message name="Simple" id="2">`)
	require.Contains(t, xmlStr, `<enum name="Side"`)
	require.Contains(t, xmlStr, `<composite name="Inner">`)
	require.Contains(t, xmlStr, `<group name="fills"`)

	// Verify it parses back.
	schema, err := sbe.ParseXMLSchema(xmlBytes)
	require.NoError(t, err)

	require.Equal(t, "test.v1", schema.Package)
	require.Equal(t, uint32(1), schema.ID)
	require.Len(t, schema.Messages, 4)

	var order *sbe.XMLMessage
	for i := range schema.Messages {
		if schema.Messages[i].Name == "Order" {
			order = &schema.Messages[i]
			break
		}
	}
	require.NotNil(t, order)
	require.Equal(t, uint32(1), order.ID)
	require.Len(t, order.Groups, 1)
	require.Equal(t, "fills", order.Groups[0].Name)
}

func TestProtoToXMLRoundTrip(t *testing.T) {
	// Proto → XML → proto → compile → codec → marshal/unmarshal.
	fd := compileTestProto(t)

	xmlBytes, err := sbe.ProtoToXML(fd)
	require.NoError(t, err)

	protoBytes, err := sbe.XMLToProto(xmlBytes)
	require.NoError(t, err)

	fd2 := compileProtoSource(t, string(protoBytes))
	codec, err := sbe.NewCodec(fd2)
	require.NoError(t, err)

	// Simple message round-trip through codec built from round-tripped schema.
	desc := fd2.Messages().ByName("Simple")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint32(42))
	msg.Set(desc.Fields().ByName("value"), protoreflect.ValueOfInt32(-100))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))
	require.Equal(t, uint64(42), got.Get(desc.Fields().ByName("id")).Uint())
	require.Equal(t, int64(-100), got.Get(desc.Fields().ByName("value")).Int())
}

func TestNameConversions(t *testing.T) {
	tests := []struct {
		camel string
		snake string
	}{
		{"orderId", "order_id"},
		{"fillPrice", "fill_price"},
		{"id", "id"},
		{"x", "x"},
		{"orderID", "order_id"},
	}
	for _, tt := range tests {
		// We can't call unexported functions directly, so test via XML round-trip.
		// This test validates the naming convention by checking generated proto output.
		xml := `<?xml version="1.0" encoding="UTF-8"?>
<sbe:messageSchema xmlns:sbe="http://fixprotocol.io/2016/sbe" package="t" id="1" version="0" byteOrder="littleEndian">
    <types>
        <composite name="messageHeader"><type name="blockLength" primitiveType="uint16"/><type name="templateId" primitiveType="uint16"/><type name="schemaId" primitiveType="uint16"/><type name="version" primitiveType="uint16"/></composite>
    </types>
    <sbe:message name="M" id="1">
        <field name="` + tt.camel + `" id="1" type="int32"/>
    </sbe:message>
</sbe:messageSchema>`
		proto, err := sbe.XMLToProto([]byte(xml))
		require.NoError(t, err, "camel=%s", tt.camel)
		require.Contains(t, string(proto), tt.snake+" = 1", "camel=%s → expected snake=%s", tt.camel, tt.snake)
	}
}

func TestEnumPrefixConversion(t *testing.T) {
	// Proto→XML should strip the enum prefix from values.
	fd := compileTestProto(t)
	xmlBytes, err := sbe.ProtoToXML(fd)
	require.NoError(t, err)

	xmlStr := string(xmlBytes)
	require.Contains(t, xmlStr, `<validValue name="Buy">0</validValue>`)
	require.Contains(t, xmlStr, `<validValue name="Sell">1</validValue>`)

	// XML→proto should add the enum prefix back.
	schema, err := sbe.ParseXMLSchema(xmlBytes)
	require.NoError(t, err)

	protoBytes, err := sbe.XMLToProto(xmlBytes)
	require.NoError(t, err)
	proto := string(protoBytes)

	_ = schema
	require.Contains(t, proto, "SIDE_BUY = 0;")
	require.Contains(t, proto, "SIDE_SELL = 1;")
}

func TestXMLWithoutNamespace(t *testing.T) {
	// SBE XML without namespace prefix should also parse.
	xml := strings.ReplaceAll(testSBEXML, "sbe:message", "message")
	xml = strings.ReplaceAll(xml, "sbe:messageSchema", "messageSchema")

	schema, err := sbe.ParseXMLSchema([]byte(xml))
	require.NoError(t, err)
	require.Equal(t, "test.v1", schema.Package)
	require.Len(t, schema.Messages, 4)
}

// A char type whose characterEncoding is not UTF-8 compatible is imported as
// bytes, so its text is carried rather than refused at run time
// (protowire#494). FIX SBE's default for char is US-ASCII, so a char with no
// encoding, or an ASCII or UTF-8 one, stays a string.
func TestXMLToProtoCharacterEncoding(t *testing.T) {
	const schema = `<?xml version="1.0" encoding="UTF-8"?>
<sbe:messageSchema xmlns:sbe="http://fixprotocol.io/2016/sbe"
                   package="enc.v1" id="2" version="0" byteOrder="littleEndian">
    <types>
        <type name="Plain" primitiveType="char" length="4"/>
        <type name="Ascii" primitiveType="char" length="4" characterEncoding="ASCII"/>
        <type name="UsAscii" primitiveType="char" length="4" characterEncoding="US-ASCII"/>
        <type name="Utf8" primitiveType="char" length="4" characterEncoding="utf_8"/>
        <type name="Latin1" primitiveType="char" length="4" characterEncoding="ISO-8859-1"/>
        <composite name="Pair">
            <type name="code" primitiveType="char" length="2" characterEncoding="windows-1252"/>
            <type name="tag" primitiveType="char" length="2"/>
        </composite>
    </types>
    <sbe:message name="Text" id="1">
        <field name="plain" id="1" type="Plain"/>
        <field name="ascii" id="2" type="Ascii"/>
        <field name="usAscii" id="3" type="UsAscii"/>
        <field name="utf8" id="4" type="Utf8"/>
        <field name="latin1" id="5" type="Latin1"/>
        <field name="pair" id="6" type="Pair"/>
    </sbe:message>
</sbe:messageSchema>`

	protoBytes, err := sbe.XMLToProto([]byte(schema))
	require.NoError(t, err)
	proto := string(protoBytes)

	for _, line := range []string{
		`string plain = 1 [(sbe.length) = 4];`,
		`string ascii = 2 [(sbe.length) = 4];`,
		`string us_ascii = 3 [(sbe.length) = 4];`,
		`string utf8 = 4 [(sbe.length) = 4];`,
		"  // SBE characterEncoding \"ISO-8859-1\" is not UTF-8 compatible, so this field is bytes.\n  bytes latin1 = 5 [(sbe.length) = 4];",
		"  // SBE characterEncoding \"windows-1252\" is not UTF-8 compatible, so this field is bytes.\n  bytes code = 1 [(sbe.length) = 2];",
		`string tag = 2 [(sbe.length) = 2];`,
	} {
		require.Contains(t, proto, line)
	}

	fd := compileProtoSource(t, proto)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	// Latin-1 "café": E9 is not UTF-8, and the bytes field carries it.
	desc := fd.Messages().ByName("Text")
	msg := dynamicpb.NewMessage(desc)
	latin1 := []byte{'c', 'a', 'f', 0xE9}
	msg.Set(desc.Fields().ByName("latin1"), protoreflect.ValueOfBytes(latin1))
	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))
	require.Equal(t, latin1, got.Get(desc.Fields().ByName("latin1")).Bytes())
}

// fixedArraySchema has the two fixed-length arrays that are not text: a char
// in a non-UTF-8 encoding and a uint8 array, each followed by a field whose
// offset moves if the array's length is lost (#249).
const fixedArraySchema = `<?xml version="1.0" encoding="UTF-8"?>
<sbe:messageSchema xmlns:sbe="http://fixprotocol.io/2016/sbe"
                   package="arr.v1" id="3" version="0" byteOrder="littleEndian">
    <types>
        <type name="Latin1" primitiveType="char" length="4" characterEncoding="ISO-8859-1"/>
        <type name="Uuid" primitiveType="uint8" length="16"/>
        <type name="Signed" primitiveType="int8" length="3"/>
        <composite name="Key">
            <type name="raw" primitiveType="uint8" length="6"/>
            <type name="seq" primitiveType="uint32"/>
        </composite>
    </types>
    <sbe:message name="Record" id="1">
        <field name="name" id="1" type="Latin1"/>
        <field name="uuid" id="2" type="Uuid"/>
        <field name="signed" id="3" type="Signed"/>
        <field name="key" id="4" type="Key"/>
        <field name="tail" id="5" type="uint32"/>
    </sbe:message>
</sbe:messageSchema>`

// A uint8 or int8 array imports as bytes of the same length, so the block
// keeps its layout; before #249 it imported as one uint8.
func TestXMLToProtoOctetArray(t *testing.T) {
	protoBytes, err := sbe.XMLToProto([]byte(fixedArraySchema))
	require.NoError(t, err)
	proto := string(protoBytes)
	for _, line := range []string{
		`bytes uuid = 2 [(sbe.length) = 16];`,
		`bytes signed = 3 [(sbe.length) = 3];`,
		`bytes raw = 1 [(sbe.length) = 6];`,
		`uint32 tail = 5;`,
	} {
		require.Contains(t, proto, line)
	}

	fd := compileProtoSource(t, proto)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Record")
	msg := dynamicpb.NewMessage(desc)
	uuid := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 0xFF}
	msg.Set(desc.Fields().ByName("uuid"), protoreflect.ValueOfBytes(uuid))
	msg.Set(desc.Fields().ByName("tail"), protoreflect.ValueOfUint32(0xA1B2C3D4))
	data, err := codec.Marshal(msg)
	require.NoError(t, err)
	// Header(8) + name(4) + uuid(16) + signed(3) + key(6+4) + tail(4).
	require.Len(t, data, 8+4+16+3+10+4)
	require.Equal(t, uuid, data[12:28])
	require.Equal(t, []byte{0xD4, 0xC3, 0xB2, 0xA1}, data[len(data)-4:])

	got := dynamicpb.NewMessage(desc)
	require.NoError(t, codec.Unmarshal(data, got))
	require.Equal(t, uuid, got.Get(desc.Fields().ByName("uuid")).Bytes())
	require.Equal(t, uint64(0xA1B2C3D4), got.Get(desc.Fields().ByName("tail")).Uint())
}

// An array of a wider primitive has no proto field that holds it, so the
// import refuses it and names it rather than misplacing the fields after it.
func TestXMLToProtoRefusesWideArray(t *testing.T) {
	for _, tc := range []struct{ name, types, field string }{
		{"field", `<type name="Quad" primitiveType="int32" length="4"/>`, `<field name="q" id="1" type="Quad"/>`},
		{"composite", `<composite name="Box"><type name="sides" primitiveType="uint16" length="2"/></composite>`, `<field name="b" id="1" type="Box"/>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := `<?xml version="1.0" encoding="UTF-8"?>
<sbe:messageSchema xmlns:sbe="http://fixprotocol.io/2016/sbe" package="w" id="1" version="0" byteOrder="littleEndian">
    <types>` + tc.types + `</types>
    <sbe:message name="M" id="1">` + tc.field + `</sbe:message>
</sbe:messageSchema>`
			_, err := sbe.XMLToProto([]byte(schema))
			require.Error(t, err)
			require.Contains(t, err.Error(), "which no proto field represents")
		})
	}
	_, err := sbe.XMLToProto([]byte(strings.Replace(fixedArraySchema, `primitiveType="uint8" length="16"`, `primitiveType="uint64" length="2"`, 1)))
	require.EqualError(t, err, `sbe: message "Record": field "uuid": type "Uuid" is an array of 2 uint64, which no proto field represents`)
}

// XML → proto → XML → proto keeps every field's proto type, its SBE
// annotations, and the block layout (#249). Before, ProtoToXML wrote a bytes
// field as a char with no encoding, which imports back as a string.
func TestXMLRoundTripFixedArrays(t *testing.T) {
	proto1, err := sbe.XMLToProto([]byte(fixedArraySchema))
	require.NoError(t, err)
	fd1 := compileProtoSource(t, string(proto1))

	xml2, err := sbe.ProtoToXML(fd1)
	require.NoError(t, err)
	require.Contains(t, string(xml2), `<type name="bytes16" primitiveType="uint8" length="16"/>`)
	require.Contains(t, string(xml2), `<field name="name" id="1" type="bytes4"/>`)
	require.Contains(t, string(xml2), `<type name="raw" primitiveType="uint8" length="6"/>`)

	proto2, err := sbe.XMLToProto(xml2)
	require.NoError(t, err)
	fd2 := compileProtoSource(t, string(proto2))

	for _, name := range []protoreflect.Name{"Record", "Key"} {
		require.Equal(t, fieldShapes(fd1.Messages().ByName(name)), fieldShapes(fd2.Messages().ByName(name)), "message %s", name)
	}

	// The same values encode to the same bytes under both schemas.
	encode := func(fd protoreflect.FileDescriptor) []byte {
		codec, err := sbe.NewCodec(fd)
		require.NoError(t, err)
		desc := fd.Messages().ByName("Record")
		msg := dynamicpb.NewMessage(desc)
		msg.Set(desc.Fields().ByName("name"), protoreflect.ValueOfBytes([]byte{'c', 'a', 'f', 0xE9}))
		msg.Set(desc.Fields().ByName("uuid"), protoreflect.ValueOfBytes([]byte("0123456789abcdef")))
		msg.Set(desc.Fields().ByName("signed"), protoreflect.ValueOfBytes([]byte{0x80, 0x7F, 0x01}))
		key := desc.Fields().ByName("key")
		km := msg.Mutable(key).Message()
		km.Set(key.Message().Fields().ByName("raw"), protoreflect.ValueOfBytes([]byte("abcdef")))
		km.Set(key.Message().Fields().ByName("seq"), protoreflect.ValueOfUint32(7))
		msg.Set(desc.Fields().ByName("tail"), protoreflect.ValueOfUint32(9))
		data, err := codec.Marshal(msg)
		require.NoError(t, err)
		return data
	}
	require.Equal(t, encode(fd1), encode(fd2))
}

// fieldShapes lists each field's number, name, proto kind and SBE
// annotations, the parts of a field that decide its type and block layout.
func fieldShapes(md protoreflect.MessageDescriptor) []string {
	var out []string
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		opts := f.Options().(interface{ String() string }).String()
		out = append(out, fmt.Sprintf("%d %s %s [%s]", f.Number(), f.Name(), f.Kind(), opts))
	}
	return out
}

// A bytes field of length 1 exports as a uint8 with length 1, which FIX SBE
// reads as a single uint8, so it imports back as uint32 with
// (sbe.encoding) = "uint8": the same one byte in the block, but not bytes.
// Pinned so a change to that reading is a decision, not an accident.
func TestXMLRoundTripBytesLengthOne(t *testing.T) {
	fd := compileProtoSource(t, `syntax = "proto3";
package one.v1;
import "sbe/annotations.proto";
option (sbe.schema_id) = 1;
message M {
  option (sbe.template_id) = 1;
  bytes b = 1 [(sbe.length) = 1];
}
`)
	xmlBytes, err := sbe.ProtoToXML(fd)
	require.NoError(t, err)
	require.Contains(t, string(xmlBytes), `<type name="bytes1" primitiveType="uint8" length="1"/>`)
	proto, err := sbe.XMLToProto(xmlBytes)
	require.NoError(t, err)
	require.Contains(t, string(proto), `uint32 b = 1 [(sbe.encoding) = "uint8"];`)
}
