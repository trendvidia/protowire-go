# Vendored canonical schema-extension protos

`protowire/schema/v1/annotations.proto` and
`protowire/schema/v1/descriptor.proto`, copied **verbatim** from the spec
repository (trendvidia/protowire), commit `8fd8440` — the commit that
renumbered the carriers into the registered block, which is the last one
to touch either file.

They are here so the carrier tests (`carrier_test.go`, protowire-go#81)
compile a schema in the RFC-001 annotation form — `@required`,
`@default(v)` — and bind the descriptor the compiler actually produces,
rather than a hand-assembled one. `annotations.proto` declares what the
annotations mean; `descriptor.proto` declares the `1327`–`1331` carriers
they lower into. Compiling either needs the v1.2 grammar, so these are
reachable only through `github.com/trendvidia/protocompile`.

Keep them in sync with the spec repo when it changes them: the numbers
in `descriptor.proto` are what `carrier.go` hand-parses, and a copy that
drifts would let this port's tests agree with themselves while
disagreeing with every other port. There is no automated check here —
the drift gate lives in the spec repo (trendvidia/protowire#243), and it
does not yet reach this directory.

Nothing in the library reads these files. `internal/deps` pins that no
library package reaches a `.proto` compiler at all; `carrier.go` walks
the carrier's wire bytes by hand for exactly that reason.
