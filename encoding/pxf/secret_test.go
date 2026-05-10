// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// secretProtoSrc mirrors the canonical pxf.Secret definition from the
// protowire spec (proto/pxf/secret.proto). It is compiled inline so the
// PXF codec tests can run without resolving external proto imports.
const secretProtoSrc = `
syntax = "proto3";
package pxf;

message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`

const secretTestProtoSrc = `
syntax = "proto3";
package secret_test.v1;

import "pxf/secret.proto";

message SecretDemo {
  string base_url = 1;
  pxf.Secret db_password = 2;
  pxf.Secret api_token = 3;
  repeated pxf.Secret backup_keys = 4;
  map<string, pxf.Secret> tenant_keys = 5;
}
`

func compileSecretProto(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":        secretTestProtoSrc,
		"pxf/secret.proto":  secretProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
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

func secretDemoDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileSecretProto(t)
	md := fd.Messages().ByName("SecretDemo")
	require.NotNil(t, md)
	return md
}

// readSecretField pulls the inner `value` string out of a Secret-typed
// field. Tests assert against this rather than touching memguard,
// since memguard handoff is the resolver's concern, not the codec's.
func readSecretField(msg protoreflect.Message, fieldName string) (value, hint, fingerprint string) {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	sub := msg.Get(fd).Message()
	d := sub.Descriptor()
	value = sub.Get(d.Fields().ByName("value")).String()
	hint = sub.Get(d.Fields().ByName("hint")).String()
	fingerprint = sub.Get(d.Fields().ByName("fingerprint")).String()
	return
}

// --- decode ---

func TestSecretScalarShorthand(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `db_password = "supersecret"`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	value, hint, fp := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Equal(t, "supersecret", value)
	assert.Empty(t, hint)
	assert.Empty(t, fp)
}

func TestSecretBlockForm(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `db_password {
  value = "supersecret"
  hint = "Postgres primary"
  fingerprint = "sha256:abc123"
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	value, hint, fp := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Equal(t, "supersecret", value)
	assert.Equal(t, "Postgres primary", hint)
	assert.Equal(t, "sha256:abc123", fp)
}

func TestSecretMixedFormsCoexist(t *testing.T) {
	desc := secretDemoDesc(t)
	// Same file mixes shorthand + block form for two different fields.
	// Catches "global state leaked between fields" regressions.
	input := `db_password = "p1"
api_token {
  value = "t1"
  hint = "external API"
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	pwVal, pwHint, _ := readSecretField(msg.ProtoReflect(), "db_password")
	tokVal, tokHint, _ := readSecretField(msg.ProtoReflect(), "api_token")
	assert.Equal(t, "p1", pwVal)
	assert.Empty(t, pwHint)
	assert.Equal(t, "t1", tokVal)
	assert.Equal(t, "external API", tokHint)
}

func TestSecretRepeatedScalarShorthand(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `backup_keys = [
  "key-a",
  "key-b",
  "key-c"
]`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("backup_keys")
	list := msg.ProtoReflect().Get(fd).List()
	require.Equal(t, 3, list.Len())

	innerName := protoreflect.Name("value")
	for i, want := range []string{"key-a", "key-b", "key-c"} {
		sub := list.Get(i).Message()
		got := sub.Get(sub.Descriptor().Fields().ByName(innerName)).String()
		assert.Equal(t, want, got, "backup_keys[%d]", i)
	}
}

func TestSecretRepeatedMixedForms(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `backup_keys = [
  "shorthand-1",
  {
    value = "block-1"
    hint = "primary"
  },
  "shorthand-2"
]`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("backup_keys")
	list := msg.ProtoReflect().Get(fd).List()
	require.Equal(t, 3, list.Len())

	get := func(i int, name string) string {
		sub := list.Get(i).Message()
		return sub.Get(sub.Descriptor().Fields().ByName(protoreflect.Name(name))).String()
	}
	assert.Equal(t, "shorthand-1", get(0, "value"))
	assert.Equal(t, "block-1", get(1, "value"))
	assert.Equal(t, "primary", get(1, "hint"))
	assert.Equal(t, "shorthand-2", get(2, "value"))
}

// --- encode ---

func TestSecretEncodeScalarWhenNoMetadata(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `db_password = "x"`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(out), `db_password = "x"`)
	assert.NotContains(t, string(out), "db_password {",
		"empty-metadata Secret must round-trip as scalar shorthand")
}

func TestSecretEncodeBlockWhenHintSet(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `db_password {
  value = "x"
  hint = "h"
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	// Block form preserves hint; scalar shorthand would silently drop it.
	assert.Contains(t, s, "db_password {")
	assert.Contains(t, s, `value = "x"`)
	assert.Contains(t, s, `hint = "h"`)
}

func TestSecretEncodeBlockWhenFingerprintSet(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `db_password {
  value = "x"
  fingerprint = "fp"
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, "db_password {")
	assert.Contains(t, s, `fingerprint = "fp"`)
}

func TestSecretRepeatedEncodeScalarWhenNoMetadata(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `backup_keys = [
  "a",
  "b"
]`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, `"a"`)
	assert.Contains(t, s, `"b"`)
	// No block form should appear in the rendered list.
	assert.NotContains(t, s, "{\n    value")
}

// --- map-value coverage ---

func TestSecretMapValueScalarShorthand(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `tenant_keys = {
  "acme": "key-a"
  "globex": "key-g"
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("tenant_keys")
	m := msg.ProtoReflect().Get(fd).Map()
	require.Equal(t, 2, m.Len())

	get := func(key string) string {
		sub := m.Get(protoreflect.ValueOfString(key).MapKey()).Message()
		return sub.Get(sub.Descriptor().Fields().ByName("value")).String()
	}
	assert.Equal(t, "key-a", get("acme"))
	assert.Equal(t, "key-g", get("globex"))
}

func TestSecretMapValueMixedForms(t *testing.T) {
	desc := secretDemoDesc(t)
	// Same map mixes shorthand + block form per entry. Catches "global
	// state across map entries" regressions.
	input := `tenant_keys = {
  "acme": "key-a"
  "globex": {
    value = "key-g"
    hint = "rotated 2026-05"
  }
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("tenant_keys")
	m := msg.ProtoReflect().Get(fd).Map()
	require.Equal(t, 2, m.Len())

	get := func(key, field string) string {
		sub := m.Get(protoreflect.ValueOfString(key).MapKey()).Message()
		return sub.Get(sub.Descriptor().Fields().ByName(protoreflect.Name(field))).String()
	}
	assert.Equal(t, "key-a", get("acme", "value"))
	assert.Empty(t, get("acme", "hint"))
	assert.Equal(t, "key-g", get("globex", "value"))
	assert.Equal(t, "rotated 2026-05", get("globex", "hint"))
}

func TestSecretMapValueEncodeScalarWhenNoMetadata(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `tenant_keys = {
  "acme": "x"
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, `acme: "x"`)
	// Map block-form for the value would look like `acme: {`.
	assert.NotContains(t, s, `acme: {`)
}

func TestSecretMapValueEncodeBlockWhenMetadata(t *testing.T) {
	desc := secretDemoDesc(t)
	input := `tenant_keys = {
  "acme": {
    value = "x"
    hint = "h"
  }
}`

	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	// Metadata forces block form so hint round-trips.
	assert.Contains(t, s, `acme: {`)
	assert.Contains(t, s, `value = "x"`)
	assert.Contains(t, s, `hint = "h"`)
}

// --- presence tracking for inner WKT fields ---

func TestSecretShorthand_MarksInnerValuePresent(t *testing.T) {
	desc := secretDemoDesc(t)
	// Scalar shorthand for a top-level pxf.Secret. Must mark BOTH the
	// parent path AND the inner `value` path present. Block form does
	// this by walking each field and calling markPresent; the
	// shorthand path used to skip the inner mark — this regression
	// guard catches that.
	input := `db_password = "x"`

	_, res, err := pxf.UnmarshalOptions{}.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)

	assert.True(t, res.IsSet("db_password"), "parent path must be present")
	assert.True(t, res.IsSet("db_password.value"),
		"inner value path must also be present under scalar shorthand")
}

func TestSecretBlockForm_MarksInnerValuePresent(t *testing.T) {
	desc := secretDemoDesc(t)
	// Sanity: block form has always marked inner present. This test
	// pins that behavior so the shorthand fix can't accidentally
	// regress block-form parsing.
	input := `db_password { value = "x" hint = "h" }`

	_, res, err := pxf.UnmarshalOptions{}.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)

	assert.True(t, res.IsSet("db_password.value"))
	assert.True(t, res.IsSet("db_password.hint"))
}

// --- regression: the existing "expected '{'" error must still fire for
// non-Secret message fields when handed a bare string. ---

func TestNonSecretMessageRejectsScalar(t *testing.T) {
	// Define a vanilla message field (not a well-known type). Feeding
	// it a bare string should still error — this guards against our
	// Secret short-circuit accidentally widening to all message fields.
	const src = `
syntax = "proto3";
package secret_test.v1;
message Inner { string s = 1; }
message Outer { Inner inner = 1; }
`
	sources := map[string]string{"test.proto": src}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	desc := result[0].Messages().ByName("Outer")

	_, err = pxf.UnmarshalDescriptor([]byte(`inner = "x"`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected '{'")
}
