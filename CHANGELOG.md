# Changelog

All notable changes to `protowire-go` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The version number is kept aligned with the rest of the `protowire-*`
stack — releases bump in lockstep across language ports when the wire
format changes.

## [Unreleased]

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

[Unreleased]: https://github.com/trendvidia/protowire-go/compare/v0.70.1...HEAD
[0.70.1]: https://github.com/trendvidia/protowire-go/compare/v0.70.0...v0.70.1
[0.70.0]: https://github.com/trendvidia/protowire-go/releases/tag/v0.70.0
