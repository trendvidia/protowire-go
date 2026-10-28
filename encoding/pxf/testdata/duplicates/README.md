# Singular-field-bound-twice fixtures

Cross-port conformance fixtures for the rule that **a singular field is
bound at most once in one message body** ([issue #347](https://github.com/trendvidia/protowire/issues/347),
decided by the owner as option (a); IETF draft `-01` § Entries and Keys).
A decoder rejects the second binding. Two blocks for one message field do
not merge, and a second scalar does not replace the first. Repeated fields
still concatenate ([#337](https://github.com/trendvidia/protowire/issues/337)).

Every fixture binds against [`duplicates.proto`](duplicates.proto) (package
`dup.v1`, message `Holder`). Each file's leading comment states the expected
behavior, and `cmd/pxf/duplicates_test.go` runs it through `Parse` (which
accepts every fixture: a second binding is legal syntax), a decoder bound to
`dup.v1.Holder`, and `pxf validate`.

| File | Verdict | Asserts |
|---|---|---|
| `err-scalar-twice.pxf` | reject | `s = "a"` then `s = "b"`. |
| `err-scalar-same-value.pxf` | reject | The same value twice is still two bindings. |
| `err-wrapper-twice.pxf` | reject | A wrapper is a singular field. |
| `err-optional-twice.pxf` | reject | So is a proto3 `optional` scalar. |
| `err-null-then-value.pxf` | reject | `null` is a binding: `w = null` then `w = 3`. |
| `err-message-twice.pxf` | reject | `m { a = 1 }` then `m { b = 2 }`: the blocks do not merge. |
| `err-message-assign-then-block.pxf` | reject | `m = { … }` and `m { … }` are one binding written two ways. |
| `err-nested-scalar-twice.pxf` | reject | The rule holds inside a sub-message's body. |
| `err-oneof-member-twice.pxf` | reject | A oneof message member bound twice. |
| `err-oneof-scalar-twice.pxf` | reject | A oneof scalar member bound twice. |
| `once-per-body.pxf` | accept | Each body is its own scope: `a` once in `m`, once in `n`, once per list element. |
| `repeated-twice.pxf` | accept | A repeated field bound twice concatenates. |

Every port was measured on 2026-10-27 (the table on #347), and no port
met all of these. Every port but Swift keeps the last scalar. For two
blocks, Go, C++, Python, C# and Dart merge them, Java, Kotlin, Rust and
TypeScript keep the last, and Swift keeps the first. Each port is filed against this
corpus.
