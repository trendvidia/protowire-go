# Changelog

All notable changes to `protowire-go` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The version number is kept aligned with the rest of the `protowire-*`
stack — releases bump in lockstep across language ports when the wire
format changes.

## [Unreleased]

## [0.72.0] — 2026-05-11

Generic `@<directive>` grammar release. Extends the PXF text format
with optional `@<name> [<type>] [{ ... }]` blocks at document root,
in addition to the existing `@type` directive. Wire format
unchanged. Existing `@type ...` documents continue to parse
identically.

### Added

- **`@<directive>` grammar** at document root. Zero or more
  `@<name> [<type>] [{ ... }]` blocks may appear before the
  schema-typed body, in any order with `@type`. Name "type" remains
  reserved (declares the body's message type). All other names are
  user-defined. The block's inner body is parsed for syntactic
  well-formedness (string / brace / comment matching) but its
  contents are NOT decoded against any schema — they're handed back
  to the caller as raw bytes.

  Motivating use case: chameleon's `@header chameleon.v1.LayerHeader
  { id = "x" }` preamble. Before this release chameleon had to peel
  the `@header` block off the byte stream itself via a duplicate
  PXF tokenizer; now `pxf.UnmarshalFull` and `pxf.Parse` consume the
  preamble natively and expose the directive list to the caller.

- **`pxf.Directive` AST type**: `{ Pos, Name, Type, Body []byte,
  LeadingComments }`. `Body` is a slice into the original input
  containing the raw inner bytes of the `{ ... }` block (or nil for
  no-block directives).

- **`Document.Directives []Directive`** — the directives Parse saw
  at document root, in source order. Excludes `@type` (still surfaced
  via `Document.TypeURL` for backward compat).

- **`Document.BodyOffset int`** — byte offset where the schema-typed
  body begins (immediately after the last directive's closing `}` or
  token). Lets callers hash / slice the body without re-scanning.

- **`Result.Directives() []Directive`** — same shape, populated by
  `pxf.UnmarshalFull` so callers don't need to invoke Parse
  separately. Empty when the document has no `@<name>` directives.

- **`AT_DIRECTIVE` token kind** for `@<name>` where `name != "type"`.
  The token's `Value` carries the bare name (without `@`).

- **`Position.Offset int`** — byte index into the input. Populated
  on every token / AST node so callers can slice the raw stream.

### Changed

- **Lexer no longer emits `ILLEGAL` for `@<name>`** when name is a
  valid identifier and != "type". Previously such inputs failed
  immediately; now they tokenize as `AT_DIRECTIVE` and parse as
  directive blocks.

### Notes for downstream consumers

- Documents that previously parsed continue to parse with byte-
  identical results — `@type` handling is unchanged, and any body
  without `@<name>` blocks emits `len(doc.Directives) == 0`.
- Documents that previously errored on `@<name>` now succeed and
  produce a directive entry. Callers that want to reject unknown
  directives should iterate `Result.Directives()` and error on
  anything unexpected.

## [0.71.0] — 2026-05-10

Layered-configuration release. Adds `pxf.Secret` recognition,
generalizes WKT scalar shorthand to map-value position, exposes
hooks for layered-config consumers (chameleon), and fixes a
subtle presence-tracking gap in the WKT shorthand decoders.

Wire format unchanged. No breaking changes to existing exported
APIs. Semver-minor.

### Fixed

- **WKT scalar-shorthand decoders now mark inner fields present.**
  Before this fix, parsing `pw = "x"` (PXF scalar shorthand for a
  pxf.Secret-typed field) set `Secret.value` on the message but did
  not call `markPresent` for the `pw.value` path. Block-form parsing
  (`pw { value = "x" }`) always marked it. Result: presence tracking
  was inconsistent based on which surface form was used. After the
  fix, both forms produce identical `Result.PresentFields()`.

  Affects all seven WKT shorthand handlers in `decodeMsgValue`:
  Timestamp (seconds, nanos), Duration (seconds, nanos), wrapper
  types (value), BigInt (abs, negative), Decimal (unscaled, scale,
  negative), BigFloat (mantissa, exponent, prec, negative), Secret
  (value).

  `consumeListMsg` and `decodeMapInline` are unchanged: per-element
  inner-field presence isn't tracked in those contexts (the parent
  list/map field is the unit of presence).

### Added

- **`UnmarshalOptions.SkipPostDecode`** — disables the per-parse pass
  that applies `(pxf.default)` and validates `(pxf.required)` so
  callers can run those passes against a merged result instead of
  per-document. Targeted at layered-configuration libraries (e.g.
  chameleon) where:
    - A base layer may legitimately omit a required field a higher
      layer provides — per-layer validation rejects this.
    - Per-layer defaults are silently lost during merge: the
      default-filled value's path is "absent" in the layer's
      `Result`, so merge falls through and clobbers it.
  With `SkipPostDecode = true`, `UnmarshalFull` returns raw presence
  tracking only.

- **`pxf.IsRequired(fd)` and `pxf.Default(fd)`** — exported the
  `(pxf.required)` and `(pxf.default)` annotation accessors.
  Previously package-private (used only by `postDecode`); now
  available to consumers running their own merged-result passes.

- **`pxf.ApplyDefault(msg, fd, def)`** — exported the scalar default
  applier. Parses the literal `def` string (PXF form) and sets it on
  the field. The bytes/enum/message-default branches are reused, so
  consumers don't have to re-implement them.

- **`Result.PresentFields() []string`** — symmetric to
  `Result.NullFields`. Returns every path encountered during parse
  (set + null). Lets layered-config systems union per-layer presence
  into a merged-result presence set.

- **WKT scalar shorthand in `map<*, message>` value position.**
  `decodeMapInline` and `encodeMapField` now go through the same
  well-known-type shortcut path as `decodeMsgValue`/`encodeMessageField`
  (top-level fields) and `consumeListMsg`/`encodeListField` (repeated
  fields). Affects `pxf.Secret`, `pxf.BigInt`, `pxf.Decimal`,
  `pxf.BigFloat`, `google.protobuf.Timestamp`, `google.protobuf.Duration`,
  and the `*Value` wrapper types. So:

  ```
  weights     = { "a": 100, "b": -7 }              # pxf.BigInt
  tenant_keys = { "acme": "k1", "globex": "k2" }   # pxf.Secret
  expirations = { "acme": 2026-12-31T00:00:00Z }   # google.protobuf.Timestamp
  ```

  Previously these required block form per entry (`"acme": { value = "k1" }`)
  even though the same shortcut worked everywhere else. Block form
  remains valid; mixed shorthand + block in the same map literal
  works per-entry.

- **`pxf.Secret` well-known type recognition in PXF.** The PXF
  codec now treats `pxf.Secret` as a value-shaped well-known
  type, with scalar shorthand decode/encode:

  ```
  db_password = "supersecret"                  # scalar shorthand
  db_password {                                # explicit form, with metadata
    value = "supersecret"
    hint  = "Postgres primary"
    fingerprint = "sha256:abc123"
  }
  ```

  Encode preserves authoring metadata: scalar form is emitted only when
  `hint` and `fingerprint` are both empty, otherwise the block form is
  used so re-emit doesn't silently drop the metadata. Repeated
  `pxf.Secret` accepts both forms in list literals.

  The codec stays free of any memory-protection dependency — it routes
  the inner `value` field as a plain string, identical to how
  `pxf.BigInt` bytes are routed. Memory protection (mlock, encrypt at
  rest in process, wipe on destroy) is the consumer runtime's
  responsibility, out of scope for this codec.

  Canonical descriptor: `proto/pxf/secret.proto` in the
  trendvidia/protowire spec repo.

  Added to wire-compatible siblings via the `isSecret` /
  `setSecretValue` / `readSecretValue` / `secretHasMetadata` helpers in
  `encoding/pxf/wellknown.go`.

  No wire-format change; this is text-format-only sugar.

## [0.70.3] — 2026-05-06

Parser-strictness release. Stays inside the 0.70.x wire-contract line:
the wire output is unchanged, only inputs that were never schema-valid
are now rejected at parse time.

### Changed (breaking)

- **PXF parser stricter on key forms**, mirroring the upstream grammar
  tightening in
  [`trendvidia/protowire@8262bbb`](https://github.com/trendvidia/protowire/commit/8262bbb)
  (`docs/grammar.ebnf`, `docs/draft-trendvidia-protowire-00.txt`):
  - `=` (field assignment) and `{ … }` (submessage) now require an
    identifier key. Inputs like `123 = 234` or `child { 123 = 123 }`
    are now parse errors with
    `"field assignment with '=' requires an identifier key, got integer
    (\"123\"); use ':' for map entries"`.
  - `:` (map entry) is rejected at document top level — the document
    represents a proto message, never a `map<K,V>`. Use `=` for
    top-level field assignments. Map literals (`field = { 1: "x" }`)
    still work because `:` remains valid inside `{ … }` blocks.

## [0.70.2] — 2026-05-06

Documentation-only release.

### Documentation

- New §"Performance: opting into the fast path" in the README
  explaining when and how a top-level binary should add the
  `replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12`
  directive to its own `go.mod` to enable the `dynamicpb` fast path.
  Calls out the library-vs-binary distinction so libraries don't pin
  the fork transitively for their consumers.
- Updated §"Schema registry integration":
  - Refresh semantics rewritten to reflect `protoregistry/client`
    v0.70.0+ (incremental aggregate updates). Per-schema lookups via
    `Schema(...)` / `FindMessageByName` are still atomic. Namespace-
    wide lookups (`FindFileByPath`, `FindExtensionByNumber`) are
    eventually consistent — use `Pin` for decodes that need a stable
    schema view end-to-end.
  - New "Fork dependency carried by `protoregistry/client`"
    subsection: depending on `protoregistry/client` makes the
    trendvidia/protobuf-go fork mandatory at the binary level
    (the namespace registry types it uses don't exist in upstream).

## [0.70.1] — 2026-05-06

### Fixed

- Compile error in downstream consumers that depend on upstream
  `google.golang.org/protobuf` (no `dynamicpb.Message.SetUnsafe`
  method). The `fastSet` helpers in `encoding/pxf/decode_fast.go` and
  `encoding/sbe/unmarshal.go` now route through an interface
  assertion instead of calling `SetUnsafe` on the concrete type, so
  the fast path is opt-in and the package compiles against either
  backend. Without the trendvidia/protobuf-go fork, decode falls
  through to the standard `protoreflect.Message.Set` — same correctness,
  no perf regression for users who already depend on upstream.

## [0.70.0] — 2026-05-06

Initial public release. Versioned to match sibling components in the
`protowire-*` stack.

### Added

- **`encoding/pxf`** — PXF text format codec. Fast-path `Unmarshal` /
  `Marshal` for `proto.Message` and `dynamicpb.Message`, AST-preserving
  `Parse` / `FormatDocument`, `UnmarshalFull` with field-presence
  tracking (`Result.IsSet` / `IsNull` / `IsAbsent`), required-field
  validation, and `(pxf.required)` / `(pxf.default)` annotations.
- **`encoding/pb`** — schema-free protobuf binary marshaler driven by
  `protowire:"N"` struct tags. Supports scalars, nested structs,
  slices, `[]byte`, and zigzag varints via the `,zigzag` tag. Output
  is wire-compatible with proto3.
- **`encoding/sbe`** — Simple Binary Encoding codec built from a
  proto file descriptor. `Marshal` / `Unmarshal` for `proto.Message`,
  zero-allocation `View` API for direct buffer reads, and
  `proto2sbe` / `sbe2proto` schema conversion helpers.
- **`envelope`** — standard cross-system response envelope with
  builder helpers (`OK`, `Err`, `TransportErr`) and field-error
  metadata.
- **Stream framing** — length-prefixed `Encoder` / `Decoder` for
  every codec in the repo.
- Cross-port benchmark harnesses under `scripts/bench_pxf` and
  `scripts/bench_sbe`.

### Security

- Strict bounds-check on the SBE root-block and group-entry
  `blockLength`: a wire `blockLength` smaller than the schema's is
  rejected before any field read. Schema-evolution case (wire larger
  than schema) continues to work.
- Overflow-safe `numInGroup × blockLength` validation in
  `unmarshalGroup`: the bound is computed against remaining buffer
  bytes rather than via direct multiplication, eliminating the 32-bit
  `int` overflow case.
- The `Codec.View` constructor mirrors the same checks so accessor-
  level reads are guaranteed to be in-bounds when the constructor
  succeeds.
- The `View` API's trust model is now documented in package godoc:
  accessors panic on schema mismatch and are not safe for
  attacker-controlled input — `Unmarshal` / `UnmarshalDescriptor` is
  the path for untrusted bytes.

### Testing

- Adversarial-input regression tests in `encoding/sbe/security_test.go`.
- `FuzzParse` (PXF), `FuzzUnmarshal` (pb), `FuzzUnmarshal` (SBE) seeded
  with valid and pathological inputs. CI runs each target for 15s on
  every PR; longer runs are intended for nightly / release branches.

### Notes

- Ships with a `replace` directive pointing at
  [`trendvidia/protobuf-go`](https://github.com/trendvidia/protobuf-go)
  `v1.36.12`, which adds `dynamicpb.SetUnsafe` /
  `AppendUnsafe` / `MapSetUnsafe`. These are the unsafe-typed setters
  used on the unmarshal hot paths.
- Minimum Go version is `1.25` (set by the floor of transitive
  dependencies, surfaced by `go mod tidy`).

[Unreleased]: https://github.com/trendvidia/protowire-go/compare/v0.70.2...HEAD
[0.70.2]: https://github.com/trendvidia/protowire-go/compare/v0.70.1...v0.70.2
[0.70.1]: https://github.com/trendvidia/protowire-go/compare/v0.70.0...v0.70.1
[0.70.0]: https://github.com/trendvidia/protowire-go/releases/tag/v0.70.0
