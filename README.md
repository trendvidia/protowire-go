# protowire-go

[![CI](https://github.com/trendvidia/protowire-go/actions/workflows/ci.yml/badge.svg)](https://github.com/trendvidia/protowire-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/trendvidia/protowire-go.svg)](https://pkg.go.dev/github.com/trendvidia/protowire-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/trendvidia/protowire-go)](https://goreportcard.com/report/github.com/trendvidia/protowire-go)
[![codecov](https://codecov.io/gh/trendvidia/protowire-go/branch/main/graph/badge.svg)](https://codecov.io/gh/trendvidia/protowire-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Go implementation of the **PXF** text format, the `pb` schema-free protobuf binary marshaler, the `sbe` (Simple Binary Encoding) codec, and the shared response envelope.

The format spec, grammar, canonical proto schemas, editor plugins, and cross-port test fixtures live in the sibling repo [trendvidia/protowire](https://github.com/trendvidia/protowire). Read that first for *what* PXF is; this README covers *how* to use it from Go.

## Install

```bash
go get github.com/trendvidia/protowire-go
```

The shared `protowire` CLI is published from a sibling repo — see the [Command-line tool](#command-line-tool) section below.

## PXF Go API

```go
import "github.com/trendvidia/protowire-go/encoding/pxf"
```

### Unmarshal

```go
// With a concrete proto message
var config serverpb.ServerConfig
err := pxf.Unmarshal(data, &config)

// With a dynamic message from a descriptor
msg, err := pxf.UnmarshalDescriptor(data, messageDescriptor)

// With options (Any support, discard unknown fields)
opts := pxf.UnmarshalOptions{
    TypeResolver:   myResolver,
    DiscardUnknown: true,
}
msg, err := opts.UnmarshalDescriptor(data, desc)
```

### UnmarshalFull (field presence, required, defaults)

`UnmarshalFull` returns a `Result` that tracks which fields were set, null, or absent. It also validates required fields and applies defaults declared via `(pxf.required)` / `(pxf.default)` annotations.

```go
result, err := pxf.UnmarshalFull(data, msg)

result.IsSet("name")      // true — field has a concrete value
result.IsNull("email")    // true — field was explicitly set to null
result.IsAbsent("role")   // true — field was not mentioned
result.NullFields()       // ["email"]
```

### Marshal

```go
data, err := pxf.Marshal(msg)

// With options
data, err := pxf.MarshalOptions{
    TypeURL:      "mypackage.v1.ServerConfig",
    TypeResolver: myResolver,   // for Any fields
    EmitDefaults: true,
}.Marshal(msg)
```

### Parse (AST with comments)

```go
doc, err := pxf.Parse(data)
// doc.TypeURL, doc.Entries, doc.LeadingComments

// Comment-preserving format
output := pxf.FormatDocument(doc)
```

### Decoder paths

- **Fast path** (`Unmarshal`): fused single-pass lexer+decoder, zero-copy token strings, no AST allocation. Writes directly to the `proto.Message`.
- **AST path** (`Parse` + `FormatDocument`): recursive-descent parser that attaches comments to AST entries. Use this when you need to round-trip a document with its comments preserved.

## Struct binary marshaling (`encoding/pb`)

Marshal any Go struct to/from protobuf binary using `protowire:"N"` struct tags — no `.proto` files, no code generation.

```go
import "github.com/trendvidia/protowire-go/encoding/pb"

type Endpoint struct {
    Path   string `protowire:"1"`
    Method string `protowire:"2"`
    Port   int    `protowire:"3"`
}

type Config struct {
    Hostname  string      `protowire:"1"`
    Enabled   bool        `protowire:"2"`
    Endpoints []*Endpoint `protowire:"3"`
    Data      []byte      `protowire:"4"`
}

// Encode
data, err := pb.Marshal(&Config{
    Hostname: "web-01",
    Enabled:  true,
    Endpoints: []*Endpoint{
        {Path: "/api", Method: "GET", Port: 8080},
    },
})

// Decode
var cfg Config
err = pb.Unmarshal(data, &cfg)
```

The output is standard protobuf binary — wire-compatible with any `.proto` definition using the same field numbers. Proto3 semantics: zero-value fields are omitted.

### Supported types

| Go type | Wire type | Encoding |
|---------|-----------|----------|
| `bool` | varint | 0/1 |
| `int`, `int64`, `int32`, `int16`, `int8` | varint | proto3 `int32`/`int64` (negatives sign-extend to 10-byte varint) |
| `int*` with `,zigzag` tag | varint | proto3 `sint32`/`sint64` (compact for negatives) |
| `uint`, `uint64`, `uint32`, `uint16`, `uint8` | varint | unsigned |
| `float64` | fixed64 | IEEE 754 |
| `float32` | fixed32 | IEEE 754 |
| `string` | bytes | length-prefixed |
| `[]byte` | bytes | length-prefixed |
| struct, *struct | bytes | embedded message |
| slice of any above | repeated | one tag+value per element |

Named types (e.g., `type Status uint8`) follow their underlying kind. Fields without a `protowire` tag are ignored.

## SBE codec (`encoding/sbe`)

```go
import "github.com/trendvidia/protowire-go/encoding/sbe"

// Create codec from proto file descriptor (compile via protocompile or load from a registry)
codec, err := sbe.NewCodec(fileDescriptor)

// Encode proto.Message to SBE binary
data, err := codec.Marshal(msg)

// Decode SBE binary into proto.Message
err = codec.Unmarshal(data, msg)
```

### View API (zero-allocation reads)

For maximum performance, the `View` API reads fields directly from the SBE buffer at pre-computed offsets with zero allocations:

```go
v, err := codec.View(data)

// Scalars — direct buffer reads, no allocations
orderID := v.Uint("order_id")
symbol  := v.String("symbol")   // zero-copy, backed by buffer
price   := v.Int("price")

// Repeating groups
fills := v.Group("fills")
for i := range fills.Len() {
    e := fills.Entry(i)
    _ = e.Int("fill_price")
    _ = e.Uint("fill_qty")
}

// Composites (nested messages)
inner := v.Composite("inner")
x := inner.Int("x")
```

Strings returned by `View.String` point into the original buffer via `unsafe.String` and are only valid while that buffer is alive.

## Schema registry integration

Pair `protowire-go` with [`protoregistry/client`](https://github.com/trendvidia/protoregistry/tree/v0.70.0/client) when the message descriptor isn't compiled into the binary — e.g. a Go service that decodes payloads whose `.proto` lives in a runtime schema registry. The client is namespace-scoped and implements protobuf-go's standard resolver interfaces (`protoreflect.MessageTypeResolver`, `protodesc.Resolver`), so descriptors fetched from the registry drop straight into `pxf.UnmarshalDescriptor`, `sbe.NewCodec`, `protojson.UnmarshalOptions{Resolver: ...}`, and `anypb` without adapter code.

```go
import (
    "context"

    "google.golang.org/protobuf/reflect/protoreflect"

    "github.com/trendvidia/protoregistry/client"
    "github.com/trendvidia/protowire-go/encoding/pxf"
)

ctx := context.Background()
r, err := client.Dial(ctx, "registry.internal:50051", "billing")
if err != nil { /* ... */ }
defer r.Close()

desc, err := r.FindDescriptorByName("billing.v1.Config")
if err != nil { /* ... */ }

msg, err := pxf.UnmarshalDescriptor(pxfBytes, desc.(protoreflect.MessageDescriptor))
```

The `Resolver` also drops into `protojson` and `anypb` directly:

```go
opts := protojson.UnmarshalOptions{Resolver: r}
err := opts.Unmarshal(jsonBytes, msg)
```

Behavior worth knowing:

- **Eager population.** `Dial` / `client.New` fetches every schema in the namespace up front, so lookup misses surface at startup, not in the request path. Restrict to a subset with `client.WithSchemas("foo", "bar")`.
- **Polling refresh** (default 30s, configurable via `client.WithRefreshInterval`). A background goroutine re-fetches only schemas whose current version advanced. Hot-swaps are atomic — readers in flight see a consistent snapshot. Failures are logged and survived (stale-while-error). Force a refresh with `r.Refresh(ctx)`.
- **`Pin(ctx, map[string]uint64)`** returns a derived `Resolver` frozen at a specific (`schemaID` → `version`) map — useful when replaying captured PXF or SBE payloads against the exact schema version they were produced with.
- **`r.Schema(schemaID)`** narrows lookups to one schema in the namespace when the caller knows which schema owns the type — cheaper and immune to cross-schema FQN collisions.

## Command-line tool

The `protowire` CLI is shared across every port and lives in the spec repo at [github.com/trendvidia/protowire/cmd/protowire](https://github.com/trendvidia/protowire/tree/main/cmd/protowire). It is written in Go and depends on this repo as a library. Install:

```bash
go install github.com/trendvidia/protowire/cmd/protowire@latest
```

See the [spec repo README](https://github.com/trendvidia/protowire#cli) for subcommands (encode / decode / validate / fmt / sbe2proto / proto2sbe) and registry-mode flags.

## Benchmarks

### PXF text format

Apple M1, dynamic messages (`dynamicpb.Message`) — the realistic path for schema-registry-backed workflows. PXF uses a single-pass fused lexer+decoder with zero-copy token strings and no intermediate AST allocations.

```
goos: darwin
goarch: arm64
cpu: Apple M1

BenchmarkPXFUnmarshal       175576    6879 ns/op   94.64 MB/s    5448 B/op    105 allocs/op
BenchmarkPXFMarshal         276253    4339 ns/op                 2392 B/op     13 allocs/op
BenchmarkJSONUnmarshal      106254   11188 ns/op   51.84 MB/s    6336 B/op    151 allocs/op
BenchmarkJSONMarshal        144099    8480 ns/op                 3670 B/op     75 allocs/op
BenchmarkProtoMarshal       182311    6550 ns/op                 1986 B/op     49 allocs/op
BenchmarkProtoUnmarshal     162969    7749 ns/op   38.84 MB/s    5848 B/op    115 allocs/op
BenchmarkYAMLMarshal         36472   32840 ns/op                81178 B/op    192 allocs/op
BenchmarkYAMLUnmarshal       33252   36392 ns/op   16.65 MB/s   25940 B/op    460 allocs/op
```

| | Unmarshal | Marshal |
|---|---|---|
| **PXF** | **6.9 µs** / 105 allocs | **4.3 µs** / 13 allocs |
| JSON (protojson) | 11.2 µs / 151 allocs | 8.5 µs / 75 allocs |
| Proto binary | 7.7 µs / 115 allocs | 6.6 µs / 49 allocs |
| YAML (yaml.v3) | 36.4 µs / 460 allocs | 32.8 µs / 192 allocs |

> Proto binary is typically fastest for generated message types. On dynamic messages (the registry use case), PXF's fused single-pass decoder with zero-copy token strings outperforms proto binary's varint-tagged deserialization.

### SBE binary format

Apple M3 Max, `bench.v1.Order` (10 scalars + 3-entry repeating group). All encodings use `dynamicpb.Message` for a fair comparison. The View API reads directly from the buffer with zero allocations.

```
goos: darwin
goarch: arm64
cpu: Apple M3 Max

BenchmarkSBEMarshal        1000000    1050 ns/op                  160 B/op      1 allocs/op
BenchmarkSBEUnmarshal       190000    6700 ns/op   23.8 MB/s     2776 B/op     40 allocs/op
BenchmarkSBEViewRead       4500000     260 ns/op  614.0 MB/s        0 B/op      0 allocs/op
BenchmarkPXFMarshal         200000    6300 ns/op                 1008 B/op      5 allocs/op
BenchmarkPXFUnmarshal       120000   10300 ns/op   48.7 MB/s     2776 B/op     40 allocs/op
BenchmarkProtoMarshal       160000    7000 ns/op                  857 B/op     29 allocs/op
BenchmarkProtoUnmarshal     105000   11500 ns/op    9.3 MB/s     3192 B/op     54 allocs/op
```

| | Marshal | Decode (into proto.Message) | Decode (View, zero-alloc) |
|---|---|---|---|
| **SBE** | **1.0 µs** / 1 alloc | **6.7 µs** / 40 allocs | **260 ns** / 0 allocs |
| PXF | 6.3 µs / 5 allocs | 10.3 µs / 40 allocs | -- |
| Proto binary | 7.0 µs / 29 allocs | 11.5 µs / 54 allocs | -- |

SBE marshal is ~6× faster than PXF and ~7× faster than protobuf. The View API is ~25× faster than SBE Unmarshal and ~44× faster than protobuf — zero allocations, direct offset reads from the buffer.

### Cross-port positioning

The Go ports of PXF and SBE compete with C++, Rust, Java, and TypeScript on the same canonical fixtures. See the [cross-port benchmark table](https://github.com/trendvidia/protowire#cross-port-benchmarks) in the spec repo.

The Go PXF and SBE unmarshal paths use `dynamicpb.SetUnsafe` / `AppendUnsafe` / `MapSetUnsafe` (additions in our `protobuf-go` fork at `../protobuf-go`) to skip the per-field type-check when writing into a dynamic message. On the SBE bench fixture this cut Go SBE unmarshal latency by ~19 % (1.30 µs → 1.05 µs); on PXF it shaved ~9 % off (6.06 µs → 5.52 µs). `Marshal` is unchanged.

## Roadmap: further Go-side optimizations

The remaining headroom on Go's PXF/SBE unmarshal is in `dynamicpb` internals, not in our codec dispatch. Items below are listed in expected bang-for-buck order; numbers are estimates for the current `bench.v1.Order` / `bench.v1.Config` fixtures on Apple M1. None are required for correctness — the codec is already wire-compatible with all five ports — but they would close more of the gap to the C++ implementation.

1. **Pool group-entry messages.** After the `SetUnsafe` work, `dynamicpb.NewMessage` is the single largest remaining alloc (~49 % of allocations and ~3 per call: 1 root + 2 group entries on the SBE bench). A `sync.Pool` keyed by `MessageDescriptor`, exposed via `dynamicpb.GetPooled(desc) / PutPooled(*Message)`, could halve that. The thorny part is ownership: pooled entries escape into the user's owned message tree, so the API must either hand-back via an explicit `Reset()` contract or limit pooling to short-lived "scratch" entries cleared after marshal. Estimated win: 10–15 % SBE unmarshal, 3–5 % PXF unmarshal. *~3–4 h.*

2. **Pre-build a typed setter per `fieldTemplate` at codec build time.** Replace the `switch ft.encoding { case encInt8: ... }` ladder in `sbe.readField` with a pre-computed `func(buf []byte, off int, msg protoreflect.Message)` closure stored on the template. Saves the encoding-switch and the inner `fd.Kind()` switch in `setIntField` / `setUintField` / `setFloatField`. PXF has the symmetric `consumeScalar` switch; same idea. Doesn't reduce allocations, only branch-mispredict and call overhead. Estimated win: 5–10 %. *~2 h per codec.*

3. **Pre-grow the group list's backing slice.** SBE's group header carries `numInGroup` before the entries; we know the final list length before the first `Append`. A small `dynamicList.Grow(n)` addition to the fork (mirroring the `SetUnsafe` pattern) would pre-size the slice and avoid `append` growth re-allocs. Estimated win: 2–5 % on group-heavy payloads. *~1 h (fork commit + assertion in our codec).*

4. **`dynamicpb.NewMessageWithCapacity(desc, fieldHint int)` in the fork.** Today `NewMessage` lazy-initializes the `m.known` `map[FieldNumber]Value` on the first `Set`; the map then grows from zero through several rehash steps. Pre-sizing it from the template's known field count would skip those rehashes. Marginal — likely 1–2 %. *~30 min.*

5. **Investigate a fork-side `SetField` that writes via array indexing rather than a map.** `m.known` is a map keyed by field number; for messages whose fields are densely numbered (almost all of them), an array-backed storage is faster on both reads and writes. This is a deeper change — `dynamicpb.Message` would need a hybrid storage strategy and the change touches Marshal, Range, IsValid, Has, Get, Set. Likely needs upstream coordination to avoid forking divergence. Estimated win: 5–10 %. *~1 d.*

(Also considered and rejected: pooling the input buffer / unsafe-aliasing wire bytes for `char` SBE fields. `msg.Set` stores the slice without copying it, so any later write into the buffer corrupts the field — soundness loss isn't worth the speed.)

## Repository layout

```
protowire-go/
├── encoding/
│   ├── pb/               # Schema-free struct ↔ protobuf binary
│   ├── pxf/              # PXF text ↔ proto.Message
│   └── sbe/              # SBE binary ↔ proto.Message + zero-alloc View API
├── envelope/             # Standard response envelope helpers
├── scripts/
│   ├── bench_pxf/        # Per-port PXF bench binary (consumed by ../protowire/scripts/cross_pxf_bench.sh)
│   ├── bench_sbe/        # Per-port SBE bench binary
│   └── dump_envelope/    # Cross-port envelope-equality dumper
├── go.mod
└── go.sum
```

## Related repositories

- [trendvidia/protowire](https://github.com/trendvidia/protowire) — format spec, grammar, canonical schemas, cross-port fixtures, **and the shared CLI**
- [trendvidia/protoregistry](https://github.com/trendvidia/protoregistry) — schema registry service plus `protoregistry/client`, the Go SDK for runtime descriptor resolution that pairs with this library (see [Schema registry integration](#schema-registry-integration))
- [trendvidia/protobuf-go](https://github.com/trendvidia/protobuf-go) — `google.golang.org/protobuf` fork carrying the `dynamicpb.SetUnsafe` / `AppendUnsafe` / `MapSetUnsafe` additions used on the unmarshal hot paths

## Limitations & open gaps

This is the canonical reference port — when behavior is ambiguous, this implementation defines it. That said, a few items are explicit non-goals or future work:

- **`sint32` / `sint64` are off the dynamicpb fast path.** The hot-path optimization reaches for `SetUnsafe` on the standard kinds; the signed-zigzag varieties go through the slow path. Latency-sensitive callers using `sint*` should benchmark before committing.
- **PXF triple-quoted string dedent** uses the closing-line indent as the base; mixing tabs and spaces in the same block is implementation-defined (matches the spec's "implementation choice" carve-out — reasonable callers should avoid it).
- **SBE XML schema generation is one-way at runtime.** `proto2sbe` emits an XML schema from annotated `.proto`, but consuming a hand-authored XML schema and producing `.proto` is driven by the shared CLI rather than this runtime library. Programmatic consumers should generate at build time.
- **The CLI lives in [trendvidia/protowire/cmd/protowire](https://github.com/trendvidia/protowire/tree/main/cmd/protowire), not here.** The library here is consumed by that CLI; standalone Go callers don't need it.

## Contributing

Pull requests are welcome. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the
development workflow, code-style expectations, and the project's review
and merge process. Security-sensitive reports should follow
[`SECURITY.md`](SECURITY.md) instead of the public issue tracker.

Participation in this project's spaces is governed by
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
