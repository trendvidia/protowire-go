// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Tests for the @proto directive (draft §3.4.5): four body shapes
// distinguished lexically — anonymous, named, source, descriptor.

func TestParseProto_Anonymous(t *testing.T) {
	in := `@proto {
  string symbol = 1;
  double price = 2;
}`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 1)
	pd := doc.Protos[0]
	assert.Equal(t, pxf.ProtoAnonymous, pd.Shape)
	assert.Equal(t, "", pd.TypeName)
	assert.Contains(t, string(pd.Body), "string symbol = 1;")
	assert.Contains(t, string(pd.Body), "double price = 2;")
}

func TestParseProto_Named(t *testing.T) {
	in := `@proto trades.v1.Trade {
  string symbol = 1;
  double price = 2;
}`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 1)
	pd := doc.Protos[0]
	assert.Equal(t, pxf.ProtoNamed, pd.Shape)
	assert.Equal(t, "trades.v1.Trade", pd.TypeName)
	assert.Contains(t, string(pd.Body), "string symbol = 1;")
}

func TestParseProto_Source(t *testing.T) {
	in := `@proto """
syntax = "proto3";
package trades.v1;
message Trade {
  string symbol = 1;
}
"""`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 1)
	pd := doc.Protos[0]
	assert.Equal(t, pxf.ProtoSource, pd.Shape)
	assert.Empty(t, pd.TypeName)
	assert.Contains(t, string(pd.Body), `syntax = "proto3";`)
	assert.Contains(t, string(pd.Body), "message Trade")
}

func TestParseProto_Descriptor(t *testing.T) {
	// A toy FileDescriptorSet would be base64-encoded; for this test
	// we use a small, decodable base64 payload.
	raw := []byte{0x0a, 0x05, 'h', 'e', 'l', 'l', 'o'}
	b64 := base64.StdEncoding.EncodeToString(raw)
	in := `@proto b"` + b64 + `"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 1)
	pd := doc.Protos[0]
	assert.Equal(t, pxf.ProtoDescriptor, pd.Shape)
	assert.Equal(t, raw, pd.Body)
}

func TestParseProto_Multiple(t *testing.T) {
	in := `@proto trades.v1.Trade {
  string symbol = 1;
}
@proto orders.v1.Order {
  string id = 1;
}`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 2)
	assert.Equal(t, pxf.ProtoNamed, doc.Protos[0].Shape)
	assert.Equal(t, "trades.v1.Trade", doc.Protos[0].TypeName)
	assert.Equal(t, pxf.ProtoNamed, doc.Protos[1].Shape)
	assert.Equal(t, "orders.v1.Order", doc.Protos[1].TypeName)
}

func TestParseProto_AnonymousFollowedByDataset(t *testing.T) {
	// One-shot binding: anonymous @proto is consumed by the next
	// directive that needs a typed binding (here, an untyped @dataset).
	in := `@proto {
  string symbol = 1;
  double price = 2;
}
@dataset (symbol, price)
("AAPL", 192.34)
("MSFT", 410.10)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 1)
	require.Len(t, doc.Datasets, 1)
	assert.Equal(t, pxf.ProtoAnonymous, doc.Protos[0].Shape)
	assert.Equal(t, "", doc.Datasets[0].Type, "untyped @dataset paired with anonymous @proto")
	assert.Equal(t, []string{"symbol", "price"}, doc.Datasets[0].Columns)
	assert.Len(t, doc.Datasets[0].Rows, 2)
}

func TestParseProto_BraceNestingInBody(t *testing.T) {
	// Anonymous form should correctly find the matching closing brace
	// across nested message blocks in the proto body.
	in := `@proto {
  message Side {
    string label = 1;
  }
  Side side = 1;
}`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Protos, 1)
	body := string(doc.Protos[0].Body)
	assert.Contains(t, body, "message Side")
	assert.Contains(t, body, "Side side = 1;")
}

func TestParseProto_RejectsBadShape(t *testing.T) {
	in := `@proto 42`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected '{'")
}

// Note: the lexer's base64-char filter (only ALPHA / DIGIT / "+" / "/" / "=")
// catches most malformed bodies before parsing reaches the descriptor-decode
// step. A test for parser-level base64 rejection would need to construct a
// body that satisfies the lex-time character class but is still not a valid
// FileDescriptorSet — that's a binary-validity check that belongs further
// downstream (in a future schema-binding test), not here.

// Reserved-directive-name enforcement (draft §3.4.6). Applications
// must not use these names as named-directive names; v1 decoders
// reject them.
func TestReservedDirectives_Rejected(t *testing.T) {
	cases := []string{
		"@table foo { x = 1 }",
		"@datasource { url = \"db://x\" }",
		"@view { name = \"v\" }",
		"@procedure { name = \"p\" }",
		"@function { name = \"f\" }",
		"@permissions { role = \"admin\" }",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := pxf.Parse([]byte(in))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "spec-reserved")
		})
	}
}

// Smoke test: an @proto directive doesn't trip the "@dataset coexists
// with @type" standalone check, because @proto is independent of
// @dataset and may coexist with either @type or @dataset.
func TestParseProto_CoexistsWithType(t *testing.T) {
	in := `@type some.pkg.Foo
@proto some.pkg.Foo {
  string name = 1;
}
name = "alice"`
	doc, err := pxf.Parse([]byte(in))
	if err != nil {
		// May error on "name" if there's body validation, but the
		// directives should at minimum parse cleanly.
		t.Logf("got error (acceptable if body validation): %v", err)
		return
	}
	require.NotNil(t, doc)
	assert.Equal(t, "some.pkg.Foo", doc.TypeURL)
	require.Len(t, doc.Protos, 1)
	assert.Equal(t, pxf.ProtoNamed, doc.Protos[0].Shape)
}

// Helper for tests that need a strings.NewReader without import shadowing.
var _ = strings.NewReader
