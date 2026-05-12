// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TypeResolver resolves protobuf type URLs to message descriptors.
// Required for encoding/decoding google.protobuf.Any fields with sugar syntax.
type TypeResolver interface {
	FindMessageByURL(url string) (protoreflect.MessageDescriptor, error)
}

// UnmarshalOptions configures PXF decoding.
type UnmarshalOptions struct {
	// TypeResolver resolves type URLs for google.protobuf.Any fields.
	// When set, Any fields use sugar syntax: @type = "..." + inline fields.
	// When nil, Any fields decode as regular messages (type_url + value).
	TypeResolver TypeResolver

	// DiscardUnknown silently skips fields not found in the schema
	// instead of returning an error.
	DiscardUnknown bool

	// SkipPostDecode disables the per-parse pass that applies
	// (pxf.default) values to absent fields and validates
	// (pxf.required) fields. Layered configuration systems (e.g.
	// chameleon) need defaults + required to apply on the MERGED
	// result, not per-layer — a base layer may legitimately omit a
	// required field that a higher layer provides, and per-layer
	// defaults silently get clobbered by merge's "absent → fall
	// through" rule. With SkipPostDecode = true, callers get raw
	// presence tracking and run their own post-merge passes via
	// [IsRequired] and [Default].
	SkipPostDecode bool

	// SkipValidate disables the per-call schema reserved-name check
	// (draft §3.13). The default behavior — running the check on every
	// decode call — is the safe one because the check catches schemas
	// that would silently produce unreachable enum values or fields.
	// Callers that have already validated their descriptors out-of-band
	// (e.g. a registry-load step that pre-screens schemas before
	// caching their descriptors) may set this to bypass the per-call
	// recheck. Validate explicitly via [ValidateDescriptor] when
	// pre-screening.
	SkipValidate bool

	// OnSecretField, if non-nil, is called for each pxf.Secret-typed
	// field whose value is supplied via scalar shorthand (the common
	// form: `pw = "x"`, list element `["a", "b"]`, map value
	// `{ "acme": "k" }`). The hook receives the dotted field path and
	// the value string. When the hook returns nil, the decoder skips
	// the standard assignment to the inner `value` field on the Secret
	// message — the caller is responsible for routing the value to
	// whatever destination honors its own memory-management contract
	// (e.g. a memguard Enclave). Presence tracking on `value` is still
	// updated so Result.IsSet reports the field as set.
	//
	// Path scheme:
	//
	//   pw = "x"                           → "pw"
	//   db { password = "x" }              → "db.password"
	//   backup_keys = ["a", "b"]           → "backup_keys[0]", "backup_keys[1]"
	//   tenant_keys = { "acme": "k" }      → "tenant_keys[acme]"
	//
	// Block-form Secrets — `pw { value = "x", hint = "h" }` — are NOT
	// routed through this hook in this release; their value still lands
	// on Secret.Value via the standard string-field path. Callers that
	// need a closed memory window for block-form secrets should
	// post-process the message (e.g. chameleon's parse.Move walker) or
	// normalize their PXF to scalar shorthand. Hint and fingerprint
	// metadata are always assigned to the proto message; they are
	// diagnostic, not sensitive.
	//
	// Errors from the hook abort the decode and propagate.
	OnSecretField func(path, value string) error
}

// UnmarshalFull decodes PXF data into msg and returns field presence metadata.
// Unlike Unmarshal, it tracks which fields are set, null, or absent,
// validates required fields, and applies default values.
func UnmarshalFull(data []byte, msg proto.Message) (*Result, error) {
	return UnmarshalOptions{}.UnmarshalFull(data, msg)
}

// UnmarshalFull decodes PXF data into msg and returns field presence metadata.
func (o UnmarshalOptions) UnmarshalFull(data []byte, msg proto.Message) (*Result, error) {
	r := msg.ProtoReflect()
	if !o.SkipValidate {
		if err := asValidationError(ValidateFile(r.Descriptor().ParentFile())); err != nil {
			return nil, err
		}
	}
	return unmarshalDirectFull(data, r, o.TypeResolver, o.DiscardUnknown, o.SkipPostDecode, o.OnSecretField)
}
