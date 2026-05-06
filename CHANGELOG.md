# Changelog

All notable changes to `protowire-go` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The version number is kept aligned with the rest of the `protowire-*`
stack — releases bump in lockstep across language ports when the wire
format changes.

## [Unreleased]

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
