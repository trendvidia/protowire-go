# Test fixtures

`buf/validate/validate.proto` is a verbatim copy of
[`proto/protovalidate/buf/validate/validate.proto`](https://github.com/bufbuild/protovalidate/blob/v1.2.2/proto/protovalidate/buf/validate/validate.proto)
from `bufbuild/protovalidate` at tag `v1.2.2`, the revision behind the
`buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go` stub this
module links (`v1.36.12-20260709200747-435963d16310.1`). It is licensed
under the Apache License 2.0 by Buf Technologies, Inc.; the header is
kept as shipped.

The test suite serves it to the compiler as source. It is not part of
this module's published surface.
`TestVendoredValidateProtoMatchesLinkedStub` fails if this copy and the
linked stub stop describing the same file, which is the cue to bump both
together.
