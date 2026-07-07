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

### Directives and `@dataset` (Result accessors)

PXF documents can carry [`@<name>` directives, `@entry` bundles, and `@dataset` rows](https://github.com/trendvidia/protowire#directives) at the document root alongside (or instead of) a message body. `UnmarshalFull` captures all three on `Result`:

```go
result, err := pxf.UnmarshalFull(data, msg)

for _, d := range result.Directives() {
    // d.Name, d.Prefixes (zero-or-more), d.Type (back-compat: populated
    // when len(Prefixes)==1), d.Body []byte (raw inner bytes of `{ ... }`)
    // — typically handed back to pxf.UnmarshalFull against a chosen
    // message, chameleon's @header pattern.
}

for _, t := range result.Datasets() {
    // t.Type, t.Columns, t.Rows []DatasetRow.
    // Each row.Cells[i] is:
    //   nil       — empty cell (field absent, pxf.default applies)
    //   *NullVal  — explicit null (field cleared per §3.9)
    //   any other — field set to that value
}
```

`Result.Directives()` excludes `@type` and `@dataset` (those have their own accessors). Order is preserved.

### `DatasetReader`: streaming `@dataset` consumption

For datasets too large to materialize, read rows from an `io.Reader` with working-set memory bounded by the size of the largest single row — not by the row sequence:

```go
tr, err := pxf.NewDatasetReader(r)
if err != nil { /* errors.Is(err, pxf.ErrNoDataset) for "no @dataset" */ }

cols := tr.Columns()
typ  := tr.Type()
hdrs := tr.Directives()    // side-channel directives before the @dataset header

for {
    row, err := tr.Next()
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    // row.Cells: []Value with the three-state mapping above.
}
```

Multi-table documents chain via `tr.Tail()`, which yields the buffered-but-unconsumed bytes followed by the remaining source:

```go
tr1, _ := pxf.NewDatasetReader(src)
// ... iterate tr1.Next() to io.EOF ...
tr2, err := pxf.NewDatasetReader(tr1.Tail())
```

Per-row arity and v1 cell-grammar errors (`[...]` / `{...}` cells, dotted columns) surface as the offending row is consumed, not deferred to end-of-input — see the [Streaming consumption](https://github.com/trendvidia/protowire/blob/main/docs/draft-trendvidia-protowire-00.txt) note in draft §3.4.4.

### `Scan` and `BindRow`: per-row binding

`Scan` reads the next row and binds its cells to a proto message by column name:

```go
for {
    var t trades.Trade
    if err := tr.Scan(&t); errors.Is(err, io.EOF) { break } else if err != nil { return err }
    process(&t)
}
```

`BindRow` is the same logic exposed standalone, for callers iterating `Result.Datasets()[i].Rows` on the materializing path:

```go
doc, _ := pxf.Parse(data)
for _, tbl := range doc.Tables {
    for _, row := range tbl.Rows {
        var t trades.Trade
        if err := pxf.BindRow(&t, tbl.Columns, row); err != nil { return err }
        process(&t)
    }
}
```

Both honor the three-state cell semantics (empty / `null` / value), bind WKT timestamps and durations, resolve enums by name, and clear wrappers / optional / oneof on a `null` cell — the implementation routes through the existing `Unmarshal` pipeline so every decoder branch is exercised. See [draft §3.4.4](https://github.com/trendvidia/protowire/blob/main/docs/draft-trendvidia-protowire-00.txt) for the spec.

### Schema reserved-name check

A protobuf schema bound for PXF use MUST NOT declare a field, oneof, or enum value named `null`, `true`, or `false` — those identifiers lex as PXF value keywords and produce silently-unreachable bindings. The check runs by default at the top of every `Unmarshal*` call:

```go
// Decoder rejects with a clear error if the schema is non-conformant.
err := pxf.Unmarshal(data, &msg)

// Inspect / pre-validate explicitly:
violations := pxf.ValidateFile(fd)      // or pxf.ValidateDescriptor(desc)
for _, v := range violations {
    fmt.Println(v.String())  // "file: enum value \"trades.v1.Side.null\" uses PXF-reserved name \"null\""
}

// Bypass per-call validation (advanced — for callers who pre-validated):
opts := pxf.UnmarshalOptions{SkipValidate: true}
err := opts.Unmarshal(data, &msg)
```

The check is case-sensitive: `NULL`, `True`, `FALSE` lex as ordinary identifiers and are accepted. See [draft §3.13](https://github.com/trendvidia/protowire/blob/main/docs/draft-trendvidia-protowire-00.txt) for the rule.

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
- **Incremental polling refresh** (default 30s, configurable via `client.WithRefreshInterval`). A background goroutine re-fetches only schemas whose current version advanced and applies the diff in place — `UpdateFile` / `UnregisterFile` for changed/removed entries, no full rebuild. Failures are logged and survived (stale-while-error). Force a refresh with `r.Refresh(ctx)`.
- **Per-schema lookups remain atomic.** `r.Schema(schemaID).FindMessageByName(...)` and `r.FindMessageByName(...)` (which routes through the cross-schema name index) read from an `atomic.Pointer`-swapped snapshot, so a single lookup always sees a coherent per-schema view.
- **Namespace-wide lookups are eventually consistent.** `r.FindFileByPath(...)` and `r.FindExtensionByNumber(...)` go through a Resolver-level aggregate that the refresh mutates incrementally. Two consecutive calls on the same `*Resolver` *can* straddle a refresh boundary and see different snapshots. For decodes that perform multiple lookups and need a stable view of the schema (e.g. nested-message dispatch), use `r.Pin(...)` (see below).
- **`Pin(ctx, map[string]uint64)`** returns a derived `Resolver` frozen at a specific (`schemaID` → `version`) map. Pinned resolvers do not refresh, build their aggregate once at Pin time, and are fully atomic. Use them for replay of captured PXF/SBE payloads, *or* whenever a single decode operation should observe one fixed schema version end-to-end.
- **`r.Schema(schemaID)`** narrows lookups to one schema in the namespace when the caller knows which schema owns the type — cheaper and immune to cross-schema FQN collisions.

### Fork dependency carried by `protoregistry/client`

`protoregistry/client` v0.70.0+ stores descriptors in
`*protoregistry.NamespacedFiles` / `*protoregistry.NamespacedTypes`, which only
exist in our [`trendvidia/protobuf-go`](https://github.com/trendvidia/protobuf-go) fork.
That means **any binary that imports `protoregistry/client` must add the same
`replace` directive to its own `go.mod`**:

```
replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12
```

Go's `replace` does not propagate across module boundaries, so depending on
`protowire-go` alone gets you the optional fast path described in the
[Performance](#performance-opting-into-the-fast-path) section below; depending
on `protoregistry/client` makes the fork mandatory at the binary level. If the
top-level `go.mod` is missing the replace, the build fails with
`undefined: protoregistry.NamespacedFiles` from the registry client.

Match the fork version pinned by both libraries (currently `v1.36.12`) when
you add the replace. They are tagged in lockstep on every fork bump.

## Command-line tool

The `protowire` CLI is shared across every port and lives in the spec repo at [github.com/trendvidia/protowire/cmd/protowire](https://github.com/trendvidia/protowire/tree/main/cmd/protowire). It is written in Go and depends on this repo as a library. Install:

```bash
go install github.com/trendvidia/protowire/cmd/protowire@latest
```

See the [spec repo README](https://github.com/trendvidia/protowire#cli) for subcommands (encode / decode / validate / fmt / sbe2proto / proto2sbe) and registry-mode flags.

## Performance: opting into the fast path

`protowire-go` works out of the box against upstream `google.golang.org/protobuf` — `go get github.com/trendvidia/protowire-go` is all you need. On the unmarshal hot path, however, it can route through three additional setters on `*dynamicpb.Message` (`SetUnsafe`, `AppendUnsafe`, `MapSetUnsafe`) that skip a per-field `Interface()` boxing allocation. Those setters live in our `google.golang.org/protobuf` fork, [trendvidia/protobuf-go](https://github.com/trendvidia/protobuf-go).

The codec selects between the two paths at runtime via an interface assertion:

- If the message implementation exposes the unsafe setters → fast path (~10–19 % lower decode latency on `dynamicpb`-backed messages on the bench fixtures, see numbers below).
- Otherwise → standard `protoreflect.Message.Set` path.

Because Go's `replace` directives [do not propagate across module boundaries](https://go.dev/ref/mod#go-mod-file-replace), depending on `protowire-go` from another module does not pull in the fork — every top-level module that wants the fast path opts in itself by adding one line to its own `go.mod`:

```
replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12
```

Run `go mod tidy` afterward and re-verify with `go test ./...`. The fork keeps the `google.golang.org/protobuf` import path, tracks upstream's tags closely, and adds nothing user-visible beyond the three unsafe setters — code that compiles against upstream compiles against the fork unchanged.

> **Contributing to `protowire-go` itself?** This repo's own `go.mod` deliberately depends only on upstream protobuf so the published module stays clean. To get the fork's fast path in your local builds and CI without committing a `replace`, use the git-ignored `go.work` workspace: `cp docs/go.work.example go.work`. See [docs/protobuf-fork.md](docs/protobuf-fork.md) for the full rationale.

When to opt in:

- **Binaries / services** that benchmark show latency or allocation pressure on `dynamicpb` decode paths — likely yes.
- **Libraries** in the middle of a dependency tree — leave the choice to the binary at the top. A library that pins the fork forces it on every downstream consumer.
- **Apps using only generated `proto.Message` types** (no `dynamicpb`) — the fast path doesn't apply, so the fork buys nothing.

The benchmark numbers in the next section are measured with the fork installed.

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
