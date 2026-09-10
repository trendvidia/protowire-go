# Test fixtures

`buf/validate/validate.proto` is a verbatim copy of
[`proto/protovalidate/buf/validate/validate.proto`](https://github.com/bufbuild/protovalidate/blob/v1.2.2/proto/protovalidate/buf/validate/validate.proto)
from `bufbuild/protovalidate` at tag `v1.2.2`. It is licensed under the
Apache License 2.0 by Buf Technologies, Inc.; the header is kept as
shipped.

The `buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go` stub
this module links is `v1.36.12-20260825204119-511051f7f437.1`, a BSR
revision from 2026-08-25 that has no git tag; its source differs from
`v1.2.2` in comments only (checked against the schema repo's last
change to the file before that date, `ec950f20`), which is why the
tagged copy is kept.

The test suite serves it to the compiler as source. It is not part of
this module's published surface.
`TestVendoredValidateProtoMatchesLinkedStub` fails if this copy and the
linked stub stop describing the same file, which is the cue to bump both
together.
