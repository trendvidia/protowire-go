// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// nestedSecretProtoSrc adds a sub-message containing a Secret so we can
// exercise the dotted-path case (db.password) end-to-end.
const nestedSecretProtoSrc = `
syntax = "proto3";
package secret_hook_test.v1;

import "pxf/secret.proto";

message DB {
  pxf.Secret password = 1;
}

message NestedSecretDemo {
  string base_url = 1;
  DB db = 2;
}
`

// recordedSecret is one observation produced by the test hook.
type recordedSecret struct {
	path  string
	value string
}

// recorder returns a hook that appends observations to *out plus a
// snapshot getter that returns them sorted by path (stable for
// assertions across map iteration order).
func recorder(out *[]recordedSecret) func(path, value string) error {
	return func(path, value string) error {
		*out = append(*out, recordedSecret{path: path, value: value})
		return nil
	}
}

func sortByPath(rs []recordedSecret) []recordedSecret {
	sort.Slice(rs, func(i, j int) bool { return rs[i].path < rs[j].path })
	return rs
}

// TestOnSecretField_TopLevelScalarShorthand — `db_password = "x"` fires
// the hook with path "db_password" and the routed value; the proto
// message has Secret.value LEFT EMPTY (since the hook took it).
func TestOnSecretField_TopLevelScalarShorthand(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	msg, err := opts.UnmarshalDescriptor([]byte(`db_password = "supersecret"`), desc)
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "db_password", got[0].path)
	assert.Equal(t, "supersecret", got[0].value)

	value, _, _ := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Empty(t, value, "hook took the value; Secret.value should be empty")
}

// TestOnSecretField_NestedDottedPath — `db { password = "x" }` fires
// with path "db.password" (the dotted form chameleon's secret.Map
// expects for nested submessages).
func TestOnSecretField_NestedDottedPath(t *testing.T) {
	desc := compileNestedDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `db {
  password = "rootpw"
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "db.password", got[0].path)
	assert.Equal(t, "rootpw", got[0].value)
}

// TestOnSecretField_RepeatedIndexedPaths — `backup_keys = ["a", "b"]`
// fires three times with paths "backup_keys[0..2]" and matching values.
func TestOnSecretField_RepeatedIndexedPaths(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	_, err := opts.UnmarshalDescriptor([]byte(`backup_keys = ["a", "b", "c"]`), desc)
	require.NoError(t, err)

	got = sortByPath(got)
	require.Len(t, got, 3)
	assert.Equal(t, "backup_keys[0]", got[0].path)
	assert.Equal(t, "a", got[0].value)
	assert.Equal(t, "backup_keys[1]", got[1].path)
	assert.Equal(t, "b", got[1].value)
	assert.Equal(t, "backup_keys[2]", got[2].path)
	assert.Equal(t, "c", got[2].value)
}

// TestOnSecretField_MapQuotedKeyPaths — map<string, pxf.Secret> fires
// the hook with paths like `tenant_keys["acme"]` (the chameleon
// pathfmt convention; string keys are quoted).
func TestOnSecretField_MapQuotedKeyPaths(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `tenant_keys = {
  "acme": "k1"
  "evil_corp": "k2"
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	got = sortByPath(got)
	require.Len(t, got, 2)
	assert.Equal(t, `tenant_keys["acme"]`, got[0].path)
	assert.Equal(t, "k1", got[0].value)
	assert.Equal(t, `tenant_keys["evil_corp"]`, got[1].path)
	assert.Equal(t, "k2", got[1].value)
}

// TestOnSecretField_HookErrorAbortsDecode — a non-nil error from the
// hook aborts the decode and propagates.
func TestOnSecretField_HookErrorAbortsDecode(t *testing.T) {
	desc := secretDemoDesc(t)
	wantErr := errors.New("vault unreachable")
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return wantErr },
	}

	_, err := opts.UnmarshalDescriptor([]byte(`db_password = "x"`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault unreachable")
	assert.Contains(t, err.Error(), "db_password")
}

// TestOnSecretField_HookNotSet_BackwardCompatible — when OnSecretField
// is nil, the existing behavior is unchanged: Secret.value is set on
// the proto message.
func TestOnSecretField_HookNotSet_BackwardCompatible(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{} // hook nil

	msg, err := opts.UnmarshalDescriptor([]byte(`db_password = "supersecret"`), desc)
	require.NoError(t, err)

	value, _, _ := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Equal(t, "supersecret", value)
}

// TestOnSecretField_BlockFormFiresHook — `pw { value = "x" }` now
// routes value through the hook the same way scalar shorthand does
// (since the block-form heap-window closure shipped). The proto
// message's Secret.value is left empty; hint and fingerprint are
// assigned to the Secret message normally.
func TestOnSecretField_BlockFormFiresHook(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `db_password {
  value = "supersecret"
  hint = "Postgres primary"
}`
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	require.Len(t, got, 1, "block form should fire the hook once for the value subfield")
	assert.Equal(t, "db_password", got[0].path)
	assert.Equal(t, "supersecret", got[0].value)

	value, hint, _ := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Empty(t, value, "block-form value should NOT land on Secret.value when hook took it")
	assert.Equal(t, "Postgres primary", hint, "hint is diagnostic; still assigned normally")
}

// TestOnSecretField_MixedFormsBothFireHook — shorthand AND block form
// both fire the hook now. Two hook calls for the two Secret fields,
// neither lands on Secret.value, hint remains accessible on the
// block-form Secret's message.
func TestOnSecretField_MixedFormsBothFireHook(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `db_password = "p1"
api_token {
  value = "t1"
  hint = "external API"
}`
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	got = sortByPath(got)
	require.Len(t, got, 2, "both forms should fire the hook")
	assert.Equal(t, "api_token", got[0].path)
	assert.Equal(t, "t1", got[0].value)
	assert.Equal(t, "db_password", got[1].path)
	assert.Equal(t, "p1", got[1].value)

	pwVal, _, _ := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Empty(t, pwVal, "shorthand was routed through hook")
	tokVal, tokHint, _ := readSecretField(msg.ProtoReflect(), "api_token")
	assert.Empty(t, tokVal, "block-form value was also routed through hook")
	assert.Equal(t, "external API", tokHint, "hint still assigned to message")
}

// TestOnSecretField_PresenceMarkedOnFullUnmarshal — UnmarshalFull
// must still report Secret.value as present even though the hook
// took the value. Otherwise post-decode required-field validation
// would reject Secret-bearing messages that *did* supply the value.
func TestOnSecretField_PresenceMarkedOnFullUnmarshal(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{
		OnSecretField:  func(path, value string) error { return nil },
		SkipPostDecode: true,
	}

	_, result, err := opts.UnmarshalFullDescriptor([]byte(`db_password = "x"`), desc)
	require.NoError(t, err)
	assert.True(t, result.IsSet("db_password.value"), "hook-routed value should still mark presence")
}

// TestOnSecretField_InvalidUTF8Rejected — even when the hook is set,
// invalid UTF-8 in the secret value is rejected at the assignment
// site (same hardening rule as the standard scalar path).
func TestOnSecretField_InvalidUTF8Rejected(t *testing.T) {
	desc := secretDemoDesc(t)
	called := false
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { called = true; return nil },
	}

	// "\xff\xfe" is invalid UTF-8 (lone continuation bytes).
	_, err := opts.UnmarshalDescriptor([]byte(`db_password = "\xff\xfe"`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8")
	assert.False(t, called, "hook must not fire for invalid UTF-8 values")
}

// TestOnSecretField_RepeatedHookErrorAborts — hook error in the
// repeated-list context propagates with the indexed path so the
// caller can pinpoint which element failed.
func TestOnSecretField_RepeatedHookErrorAborts(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error {
			if value == "b" {
				return errors.New("vault rejected key")
			}
			return nil
		},
	}

	_, err := opts.UnmarshalDescriptor([]byte(`backup_keys = ["a", "b", "c"]`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault rejected key")
	assert.Contains(t, err.Error(), "backup_keys[1]")
}

// TestOnSecretField_RepeatedInvalidUTF8Rejected — invalid UTF-8 in a
// repeated-list element is caught before the hook fires.
func TestOnSecretField_RepeatedInvalidUTF8Rejected(t *testing.T) {
	desc := secretDemoDesc(t)
	called := false
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { called = true; return nil },
	}

	_, err := opts.UnmarshalDescriptor([]byte(`backup_keys = ["\xff\xfe"]`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8")
	assert.False(t, called)
}

// TestOnSecretField_MapHookErrorAborts — hook error in the map-value
// context propagates with the quoted-key path.
func TestOnSecretField_MapHookErrorAborts(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error {
			return errors.New("kms unreachable")
		},
	}

	input := `tenant_keys = {
  "acme": "k1"
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kms unreachable")
	// %q-quoting the path in the wrapping error message escapes the
	// inner quotes around "acme", so the literal bytes carry
	// `tenant_keys[\"acme\"]`. Match on the unambiguous unquoted
	// substring instead.
	assert.Contains(t, err.Error(), "tenant_keys[")
	assert.Contains(t, err.Error(), "acme")
}

// TestOnSecretField_MapInvalidUTF8Rejected — invalid UTF-8 in a map
// value is caught before the hook fires.
func TestOnSecretField_MapInvalidUTF8Rejected(t *testing.T) {
	desc := secretDemoDesc(t)
	called := false
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { called = true; return nil },
	}

	input := `tenant_keys = {
  "acme": "\xff\xfe"
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8")
	assert.False(t, called)
}

// TestOnSecretField_BlockFormNested — block-form Secret nested in a
// scalar user-message field (DB.password { value = "..." } via the
// nested fixture) routes through the hook with the dotted path.
func TestOnSecretField_BlockFormNested(t *testing.T) {
	desc := compileNestedDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `db {
  password {
    value = "rootpw"
    hint = "root account"
  }
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "db.password", got[0].path)
	assert.Equal(t, "rootpw", got[0].value)
}

// TestOnSecretField_BlockFormRepeated — block-form Secrets inside a
// repeated pxf.Secret field route through the hook with indexed
// paths.
func TestOnSecretField_BlockFormRepeated(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `backup_keys = [
  { value = "k0" hint = "first" }
  { value = "k1" }
]`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	got = sortByPath(got)
	require.Len(t, got, 2)
	assert.Equal(t, "backup_keys[0]", got[0].path)
	assert.Equal(t, "k0", got[0].value)
	assert.Equal(t, "backup_keys[1]", got[1].path)
	assert.Equal(t, "k1", got[1].value)
}

// TestOnSecretField_BlockFormMap — block-form Secrets as map values
// route through the hook with quoted-key paths.
func TestOnSecretField_BlockFormMap(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `tenant_keys = {
  "acme": { value = "k1" hint = "acme prod" }
  "globex": { value = "k2" }
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	got = sortByPath(got)
	require.Len(t, got, 2)
	assert.Equal(t, `tenant_keys["acme"]`, got[0].path)
	assert.Equal(t, "k1", got[0].value)
	assert.Equal(t, `tenant_keys["globex"]`, got[1].path)
	assert.Equal(t, "k2", got[1].value)
}

// TestOnSecretField_BlockFormHintFingerprintWithoutValue — a Secret
// block carrying only hint / fingerprint (no value) does NOT fire
// the hook. Hint / fingerprint are still assigned to the message.
func TestOnSecretField_BlockFormHintFingerprintWithoutValue(t *testing.T) {
	desc := secretDemoDesc(t)
	var got []recordedSecret
	opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}

	input := `db_password {
  hint = "rotated 2026"
  fingerprint = "sha256:abc"
}`
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	assert.Empty(t, got, "no value subfield → no hook call")
	value, hint, fp := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Empty(t, value)
	assert.Equal(t, "rotated 2026", hint)
	assert.Equal(t, "sha256:abc", fp)
}

// TestOnSecretField_BlockFormHookErrorAborts — error from the hook
// during block-form decode aborts and propagates with the path.
func TestOnSecretField_BlockFormHookErrorAborts(t *testing.T) {
	desc := secretDemoDesc(t)
	wantErr := errors.New("vault denied")
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return wantErr },
	}

	input := `db_password {
  value = "x"
  hint = "h"
}`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault denied")
	assert.Contains(t, err.Error(), "db_password")
}

// TestOnSecretField_BlockFormInvalidUTF8 — invalid UTF-8 in any of
// value / hint / fingerprint is rejected before the hook fires.
func TestOnSecretField_BlockFormInvalidUTF8(t *testing.T) {
	desc := secretDemoDesc(t)
	for _, field := range []string{"value", "hint", "fingerprint"} {
		t.Run(field, func(t *testing.T) {
			called := false
			opts := pxf.UnmarshalOptions{
				OnSecretField: func(path, value string) error { called = true; return nil },
			}
			input := `db_password { ` + field + ` = "\xff\xfe" }`
			_, err := opts.UnmarshalDescriptor([]byte(input), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "UTF-8")
			assert.False(t, called, "hook should not fire for invalid UTF-8 inputs")
		})
	}
}

// TestOnSecretField_BlockFormUnknownSubfield — pxf.Secret is a closed
// shape (value/hint/fingerprint). Unknown subfields error out so
// schema drift can't hide.
func TestOnSecretField_BlockFormUnknownSubfield(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	input := `db_password { value = "x" rotation = "P30D" }`
	_, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rotation")
}

// TestOnSecretField_BlockFormNotInvokedWithoutHook — when the hook is
// nil, block-form decoding still works via the generic field
// decoder and `value` lands on Secret.value as before. Backward
// compat for callers that don't opt into OnSecretField.
func TestOnSecretField_BlockFormNotInvokedWithoutHook(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{} // hook nil
	input := `db_password { value = "x" hint = "h" }`
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)
	value, hint, _ := readSecretField(msg.ProtoReflect(), "db_password")
	assert.Equal(t, "x", value)
	assert.Equal(t, "h", hint)
}

// TestOnSecretField_BlockFormPresenceMarked — UnmarshalFull's Result
// records presence on the block-form Secret's value / hint /
// fingerprint subfields, same as the scalar-shorthand path.
func TestOnSecretField_BlockFormPresenceMarked(t *testing.T) {
	desc := secretDemoDesc(t)
	opts := pxf.UnmarshalOptions{
		OnSecretField:  func(path, value string) error { return nil },
		SkipPostDecode: true,
	}
	input := `db_password { value = "x" hint = "h" }`
	_, result, err := opts.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)
	assert.True(t, result.IsSet("db_password.value"))
	assert.True(t, result.IsSet("db_password.hint"))
	assert.False(t, result.IsSet("db_password.fingerprint"))
}

// --- helpers ---

// compileNestedDesc compiles nestedSecretProtoSrc against the same
// pxf/secret.proto shim used by the rest of the secret tests and
// returns the NestedSecretDemo descriptor.
func compileNestedDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"nested.proto":     nestedSecretProtoSrc,
		"pxf/secret.proto": secretProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "nested.proto")
	require.NoError(t, err)
	for _, f := range result {
		if f.Path() == "nested.proto" {
			md := f.Messages().ByName("NestedSecretDemo")
			require.NotNil(t, md)
			return md
		}
	}
	t.Fatal("nested.proto not found")
	return nil
}
