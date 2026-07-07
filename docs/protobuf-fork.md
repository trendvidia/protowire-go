# The protobuf fork and the `go.work` opt-in

`protowire-go` can route its `dynamicpb` unmarshal hot path through three extra
setters — `SetUnsafe`, `AppendUnsafe`, `MapSetUnsafe` — that skip a per-field
`Interface()` boxing allocation. Those setters live only in our
[`trendvidia/protobuf-go`](https://github.com/trendvidia/protobuf-go) fork of
`google.golang.org/protobuf`. The codec picks the path at runtime via an
interface assertion (see `encoding/pxf/decode_fast.go`): if the message
implementation exposes the unsafe setters it takes the fast path, otherwise it
falls back to the standard `protoreflect.Message.Set`. **Code that compiles
against upstream compiles against the fork unchanged.**

## Why the fork is *not* a `replace` in this repo's `go.mod`

It used to be. The published `go.mod` no longer carries:

```
replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12
```

for three reasons:

1. **`replace` doesn't propagate across module boundaries.** A `replace` in
   `protowire-go`'s `go.mod` only affects builds *of this module*. Anyone who
   runs `go get github.com/trendvidia/protowire-go` never sees it, so it never
   bought downstream consumers anything — it only changed our own builds.
2. **It's a pure optimization with a graceful fallback.** Because the fast path
   is opt-in at runtime, upstream protobuf works correctly; the fork only
   changes performance. A published library should depend on the canonical
   `google.golang.org/protobuf`, not force a fork on every consumer.
3. **The fork keeps the canonical import path.** The fork's own `go.mod` still
   declares `module google.golang.org/protobuf`, so the *only* way to route to
   it is a `replace`. Renaming the fork's module path to
   `github.com/trendvidia/protobuf-go` would let us drop the `replace`, but it
   would fork the type system — `.../protoreflect.Message` from the fork and
   from upstream become distinct, non-assignable types with separate global
   `protoregistry` instances, breaking interop with every consumer that uses
   standard protobuf. So that door stays closed.

Net: the published module depends only on upstream protobuf, and downstream
consumers get the correct fallback path by default.

## Restoring the fork locally with `go.work`

To keep the fast path for **your own builds and CI** without putting the
`replace` back into `go.mod`, use a Go workspace file. `go.work` overrides
module resolution for local builds only and is **git-ignored** (see
`.gitignore`), so it never reaches the published module or any consumer.

```
cp docs/go.work.example go.work
go build ./...
go test ./...
```

A ready-to-use example lives at [`docs/go.work.example`](./go.work.example).
Its contents:

```
go 1.25.0

use .

replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12
```

Confirm the fork is actually routed in:

```
$ go list -m google.golang.org/protobuf
google.golang.org/protobuf v1.36.11 => github.com/trendvidia/protobuf-go v1.36.12
```

To build/test exactly as a downstream consumer would (upstream protobuf, no
workspace), disable the workspace for a single command:

```
GOWORK=off go test ./...
```

Keep the fork version in the `go.work` in lockstep with the tag referenced in
the README (currently `v1.36.12`); the fork tracks upstream's tags closely.

## `go.work` vs. a consumer's `go.mod` replace

| | Where | Committed? | Affects |
|---|---|---|---|
| **This repo (dev/CI)** | `go.work` (from `docs/go.work.example`) | No — git-ignored | Only local builds of this module |
| **A binary opting into the fast path** | `replace` in that binary's own `go.mod` | Yes | That binary and its build |

A *library* in the middle of a dependency tree should do neither — leave the
choice to the top-level binary. See the README's
[Performance](../README.md#performance-opting-into-the-fast-path) section for
when opting in is worthwhile.

> **Note:** binaries that import `protoregistry/client` need the `replace` in
> their `go.mod` regardless of performance — that client stores descriptors in
> `protoregistry.NamespacedFiles` / `NamespacedTypes`, which exist only in the
> fork, so the fork is *mandatory* there rather than an optimization. See the
> README's "Fork dependency carried by `protoregistry/client`" section.
