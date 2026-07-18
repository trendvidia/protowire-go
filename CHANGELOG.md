# Changelog

All notable changes to `protowire-go` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The version number is kept aligned with the rest of the `protowire-*`
stack — releases bump in lockstep across language ports when the wire
format changes.

## [Unreleased]

Reference implementation of **keyed repeated fields** (spec issue
[trendvidia/protowire#116], draft `-01` §3.13; tracked here as #50).
A `repeated <Message>` field annotated `(pxf.key) = "<field>"` may be
written as a block of named blocks — entry name = key-field value,
document order = list order. Targets the spec's v1.3.0 release train.

### Added

- `encoding/pxf`: grammar — `field_entry` accepts a string literal at
  entry-name position (`"us-east-1" { ... }`, also `"name" = { ... }`),
  in both `Parse` and `ParseTolerant`. The AST carries the unquoted
  name plus a quoted-ness flag (`Assignment.KeyQuoted`,
  `Block.NameQuoted`) so `FormatDocument` round-trips the source
  spelling. Integer keys remain map-only.
- `encoding/pxf`: decode — the Unmarshal family interprets the keyed
  block form, populating the key field from each entry name. Duplicate
  entry names in a block (compared by unquoted value), the empty string
  as a key (either surface form), a disagreeing explicit key-field
  assignment, and a quoted entry name outside a keyed field's block are
  rejected with the typed `*KeyedError` (`errors.As`-able, `Kind` one
  of `KeyedDuplicateKey` / `KeyedKeyConflict` / `KeyedEmptyKey` /
  `KeyedQuotedNameUnkeyed`). An agreeing explicit key assignment is
  redundant but legal.
- `encoding/pxf`: encode — `Marshal` emits the keyed block form
  whenever every element's key is present, non-empty, and distinct
  (entry names quoted only when not identifier-safe, key field omitted
  from entry bodies), and falls back to the anonymous list form
  otherwise.
- `encoding/pxf`: `CanonicalizeKeyed` rewrites a parsed document to
  canonical keyed form for schema-aware formatting (the reference
  `pxf fmt` pipeline together with `FormatDocument`): eligible
  anonymous bindings become keyed blocks, identifier-safe quoted names
  are unquoted, redundant agreeing key assignments are dropped.
- `encoding/pxf`: `KeyedDiagnostics` surfaces the keyed schema checks
  as positioned diagnostics over a (tolerant) AST instead of hard
  errors, for editor tooling (protolsp) and linters (protocheck).
- `encoding/pxf`: descriptor helpers `IsKeyed`, `KeyField`, and
  `KeyFieldName` expose `(pxf.key) = 50002` so downstream tools consume
  the option without re-deriving it from raw descriptors. Bind-time
  validation (`ValidateFile` / `ValidateDescriptor`, run by every
  decode unless `SkipValidate`) now also rejects invalid `(pxf.key)`
  placements as `ViolationKeyOption`, with the new `Violation.Detail`
  field carrying the explanation.
- `encoding/pxf`: the cross-port conformance corpus
  `testdata/keyed/` is vendored from the spec repo and wired into the
  test suite; fuzzing extended with a schema-bound keyed target
  (`FuzzUnmarshalKeyed`) plus keyed seeds for the parser fuzzers.

### Changed

- `encoding/pxf`: the aggregate bind-time validation error header reads
  `PXF schema violations:` (was `PXF schema reserved-name violations:`)
  now that it also covers `(pxf.key)` placement; per-violation lines
  are unchanged.
- `encoding/pxf`: the fused decoder no longer silently accepts a quoted
  string at field-name position (`"id" = ...`); it now rejects it as a
  quoted entry name outside a keyed field, matching the grammar the
  AST parser always enforced.

## [1.2.2] — 2026-07-14

Spec-conformance fix for non-finite floats in the `encoding/pxf`
decoder. No API changes.

### Fixed

- `encoding/pxf`: the decoder now accepts the non-finite float
  identifiers `inf`, `+inf`, `-inf`, and `nan` that draft §3.8 mandates
  for float and double fields — and that `Marshal` itself emits — so
  messages holding non-finite floats round-trip through the package's
  own encoder/decoder pair (#47). The signed forms lex as float tokens
  (`+` is admitted as a token start for exactly this case; `+1.5` stays
  illegal), and the bare identifiers are recognized in the decoder's
  float/double branches. Covers all decode paths, including `@dataset`
  row binding. Only the four exact lowercase spellings are accepted;
  `Inf`, `NaN`, `infinity`, signed `nan`, and finite literals that
  overflow to infinity remain rejected.

## [1.2.1] — 2026-07-09

Indentation-fidelity fixes for the `encoding/pxf` `Rewriter`. Cosmetic
only — output already round-tripped and re-parsed; no API or wire-format
changes.

### Fixed

- `encoding/pxf`: format-preserving edits now indent a nested body by the
  document's own indent width instead of a hard-coded two spaces, so
  structural insertion into a 4-space- or tab-indented document no longer
  produces mixed indentation. Both `Rewriter.AppendEntry` (#41) and the
  value path shared by `Rewriter.Set` / `Rewriter.ReplaceValue` and the
  synthesized block chains created by `Set` (#43) infer the step,
  preferring a nested sibling block's body, then a document-wide scan,
  then a two-space default.

## [1.2.0] — 2026-07-09

Additive `encoding/pxf` API for editor tooling. New exported surface
only — no changes to existing behavior or the wire format.

### Added

- `encoding/pxf`: `FormatValue` / `AppendValue` render a single
  [`Value`] to its PXF source literal (#37). This exposes the
  formatter's existing per-value logic — string escaping, raw int/float
  forms, base64 bytes, enum idents, durations, timestamps — so tools
  that splice a value back into a buffer no longer re-implement (and
  risk subtly mis-escaping) it.
- `encoding/pxf`: `Rewriter.ReplaceValue` / `Rewriter.SetSpan` mutate by
  the AST node the caller already holds from `Rewriter.Document`, rather
  than by dotted path (#38). Path resolution is first-match-wins, so it
  cannot target a specific node among same-key siblings (repeated widget
  nodes in a `children { … }` block, say); node targeting can. Both
  route through the Rewriter's edit list, preserving overlap detection,
  edit batching, and the reparse safety-net in `Rewriter.Bytes` that a
  raw byte splice discards.
- `encoding/pxf`: `Rewriter.AppendEntry` inserts a new entry into a
  block, formatted to match its existing entries — sibling-matched
  indentation, inline vs. multi-line placement, and the empty-block
  (`foo { }`) case (#39). The layout logic is the library's own, reused
  from `FormatDocument` instead of re-derived by each caller. The caller
  supplies the concrete `Entry`, so the `=` vs. `:` separator is
  explicit rather than schema-guessed.

## [1.1.2] — 2026-07-09

A parser bug fix for comment round-tripping. No API or wire-format
changes; the `encoding/pxf` AST types are unchanged.

### Fixed

- `encoding/pxf`: `Parse` no longer drops an inline (same-line) comment
  that trails the **last** entry before a block's closing `}` or EOF
  (#34). Such a comment had nowhere to attach — it was only ever flushed
  as the *next* entry's `LeadingComments`, and with no following entry it
  was lost, so `FormatDocument` of the parsed document omitted it.
  `Parse` now stores an inline-trailing comment directly in
  `Assignment.TrailingComment` / `MapEntry.TrailingComment` (the fields
  `FormatDocument` already renders), so `restrict = true # deny-by-default`
  round-trips in place. This also fixes the previously-surviving case
  where a following entry *did* exist: the comment now attaches to the
  entry it trails instead of migrating to the next entry's leading
  comments, removing the line-matching reconstruction burden on consumers
  (see trendvidia/goed#1). Comments on their own line remain leading
  comments.

## [1.1.1] — 2026-07-07

Packaging hygiene. No API or wire-format changes, and no behavior
change for consumers: a `replace` in a dependency's `go.mod` never
applied to downstream builds, so `go get github.com/trendvidia/protowire-go`
resolved to upstream protobuf before and after this release.

### Changed

- `go.mod` no longer carries `replace google.golang.org/protobuf =>
  github.com/trendvidia/protobuf-go`. The published module now depends
  only on the canonical upstream `google.golang.org/protobuf`. The
  `dynamicpb` unmarshal fast path (`SetUnsafe` / `AppendUnsafe` /
  `MapSetUnsafe`) is unchanged — still an opt-in runtime optimization
  that falls back gracefully when the fork isn't present. Contributors
  and CI restore the fork for local builds via a git-ignored `go.work`
  (copy `docs/go.work.example`); downstream binaries that want the fast
  path continue to add the `replace` to their own `go.mod`, exactly as
  before. New doc: `docs/protobuf-fork.md`.

## [1.1.0] — 2026-07-06

The editor-tooling release: the read side (error-tolerant parsing)
and write side (lossless round-trip rewriting) of machine-editing
PXF documents that humans also hand-edit. Backward-compatible; no
wire-format changes.

### Added

- `encoding/pxf`: `Rewriter` — comment- and format-preserving
  document rewriting (#24). `NewRewriter(src)` parses the source and
  stages targeted edits by dotted path: `Set` (upsert; replaces only
  the value's byte span, or inserts a new entry with sibling
  indentation) and `Remove` (deletes the entry's line(s), keeping
  leading comments). `Bytes()` splices the edits into the original
  source, so everything outside the edited spans — comments, blank
  lines, key ordering, indentation, number/string formatting quirks —
  round-trips byte-for-byte, and reparses the result as a safety net.
  `FormatDocument` remains the explicit, on-demand normalizer.
- `encoding/pxf`: AST nodes now record their end position alongside
  `Pos`: every `Value` and `Entry` carries an exclusive `End`
  position, and the new `EntrySpan` / `ValueSpan` helpers expose the
  `[start, end)` span generically. This is the span store the
  `Rewriter` splices with, and gives editor tooling precise ranges
  for hover and diagnostics.
- `encoding/pxf`: `ParseTolerant` — error-tolerant parse mode for
  editor tooling (#27). Recovers at entry/block boundaries instead of
  stopping at the first syntax error, returning a best-effort
  `*Document` plus all positioned errors: a missing or malformed value
  becomes a `BadVal` placeholder (new AST node), unclosed blocks and
  lists are closed at EOF, unterminated strings end at the newline,
  and malformed directives are skipped to the next directive or body
  entry. `Parse` keeps its all-or-nothing contract.

### Fixed

- `encoding/pxf`: `Parse`, `Unmarshal` / `UnmarshalFull`, and
  `(pxf.default)` bytes defaults now decode URL-safe base64
  (RFC 4648 §5) in `b"..."` values, matching the lexer's acceptance
  rule. Previously the lexer validated a URL-safe bytes literal but
  every decode path then rejected it.

## [1.0.0] — 2026-05-13

First major-version cut. Implements the three one-time spec changes
from the protowire v1.0 freeze line ([STABILITY.md](https://github.com/trendvidia/protowire/blob/main/STABILITY.md))
in lockstep with `protowire`, `protowire-java`, and
`protowire-typescript`. **Breaking** — there is no alias period;
v1.0 is itself the major bump.

### v1.0 spec changes

Three one-time spec changes from the protowire v1.0 freeze line.
**Breaking** — there is no alias period; v1.0 is itself the major bump.

- `@table` directive renamed to `@dataset` (draft §3.4.4). Public API
  surface follows: `TableDirective` → `DatasetDirective`, `TableRow`
  → `DatasetRow`, `TableReader` → `DatasetReader`, `NewTableReader`
  → `NewDatasetReader`, `ErrNoTable` → `ErrNoDataset`, `Document.Tables`
  → `Document.Datasets`, `Result.Tables()` → `Result.Datasets()`. Source
  files `table_*.go` renamed to `dataset_*.go`. Decoder semantics
  unchanged.

- `@proto` directive added (draft §3.4.5). New `ProtoDirective` AST node
  with `Shape` (one of `ProtoAnonymous`, `ProtoNamed`, `ProtoSource`,
  `ProtoDescriptor`), `TypeName`, and `Body`. Four body shapes
  distinguished lexically:
  - `@proto { ... }` (anonymous; body is protobuf message-body source)
  - `@proto pkg.Type { ... }` (named)
  - `@proto """..."""` (source-form .proto file)
  - `@proto b"..."` (base64-encoded `FileDescriptorSet`)
  Exposed at `Document.Protos` and `Result.Protos()`. The descriptor
  form is the MUST-support shape; the other three are QoI in this port
  (all four are supported here).

- Reserved directive names expanded from 5 to 13 (draft §3.4.6). v1
  decoders now reject `@table`, `@datasource`, `@view`, `@procedure`,
  `@function`, and `@permissions` as spec-reserved (future-allocated)
  directive names. The existing schema-level reservation (`null` /
  `true` / `false` for field/oneof/enum names; draft §3.13) is
  unchanged.

`@dataset`'s row message type is now optional in the AST. When
omitted, the directive consumes the typed binding of a preceding
anonymous `@proto` per draft §3.4.4 Anonymous binding.

## [0.77.0] — 2026-05-12

Block-form Secret decode hook. Closes the residual plaintext-in-heap
window for `pxf.Secret` block-form assignments. v0.76.0 routed
scalar shorthand (`pw = "x"`, repeated `["a","b"]`, map values
`{"k":"v"}`) through `OnSecretField` so plaintext never landed on
`Secret.value` as a Go string; the documented limitation was that
block form (`pw { value = "x", hint = "h" }`) still rode the
generic message-block decoder and left plaintext transiently on the
proto message until the downstream walker ran. v0.77.0 closes that
gap: block-form `value` now routes through the hook in all four
contexts (top-level, nested, repeated, map).

Wire format unchanged. Hook stays opt-in; the nil-hook code path is
byte-for-byte identical to v0.76.0.

### Changed

- **`UnmarshalOptions.OnSecretField` now fires for block-form
  `pxf.Secret` assignments too.** Previously documented as a
  scalar-shorthand-only hook with block form deferred to a follow-
  up. Implemented via a small custom block parser
  (`decodeSecretBlockInto`) wired into three call sites: the
  `decodeFields` LBRACE case for top-level and nested scalar-
  message Secret blocks, `consumeListMsg` for `repeated pxf.Secret`
  block elements, and `decodeMapInline` for `map<*, pxf.Secret>`
  block values.

  Behavior of the block parser:

  - Consumes `value` / `hint` / `fingerprint` subfields in any
    order; any may be absent.
  - Routes `value` through `d.onSecret` with the same dotted-path
    scheme as scalar shorthand. `Secret.value` on the proto message
    is left empty.
  - Assigns `hint` and `fingerprint` to the message normally — they
    are diagnostic, not sensitive.
  - Validates UTF-8 on every string subfield before any assignment
    or hook call.
  - Marks presence on `<path>.value` / `.hint` / `.fingerprint` so
    `UnmarshalFull`'s `Result` records the supplied subfields.
  - Rejects unknown subfields. `pxf.Secret` is closed-shape;
    tolerating extras would mask schema drift.
  - Honors `MaxNestingDepth`.

- **The `pxf.Secret`-handling comment in `decodeMsgValue`** updated
  to reflect that block form is no longer the "falls through to the
  generic decoder" case — both shorthand and block form are
  intercepted when `OnSecretField` is set.

### Coordination

- Pairs with the chameleon-side adoption (a go.mod bump to v0.77.0
  + a README note recording that the residual best-effort window
  is now closed). Chameleon's `parse.MoveInto` walker becomes a
  defensive sanity check rather than load-bearing for block-form
  Secret residency.

## [0.76.0] — 2026-05-12

Memguard-direct decode release. Adds `UnmarshalOptions.OnSecretField`
— an opt-in callback the PXF decoder fires for every `pxf.Secret`-typed
field supplied via scalar shorthand. Consumers (notably chameleon)
use it to route secret bytes into mlock'd `memguard.Enclave`s during
decode, closing the plaintext-in-heap window that previously existed
between PXF parse and the post-decode walker. Wire format unchanged;
hook is fully opt-in and the nil-hook code path is byte-for-byte
identical to v0.75.0.

### Added

- **`UnmarshalOptions.OnSecretField` callback.** When set, fires for
  every `pxf.Secret`-typed field assigned via scalar shorthand. The
  decoder hands the value string to the hook and skips the standard
  assignment to the inner `value` field on the Secret message —
  presence tracking still marks the field as set so `UnmarshalFull`'s
  required-field validation behaves identically.

  ```go
  opts := pxf.UnmarshalOptions{
      OnSecretField: func(path, value string) error {
          return enclaveStore.Seal(path, value)
      },
  }
  _, err := opts.UnmarshalFull(data, msg)
  ```

  Path scheme matches chameleon's `internal/pathfmt` byte-for-byte
  so `secret.Map` lookup keys come out identical regardless of which
  side built them:

  | PXF surface                       | path                      |
  |-----------------------------------|---------------------------|
  | `pw = "x"`                        | `pw`                      |
  | `db { password = "x" }`           | `db.password`             |
  | `backup_keys = ["a", "b"]`        | `backup_keys[0..1]`       |
  | `tenant_keys = { "acme": "k" }`   | `tenant_keys["acme"]`     |

  Fires for top-level fields, repeated-list elements, and map values.
  Block-form Secrets (`pw { value = "x", hint = "h" }`) recurse
  through the generic field decoder and the value lands on
  `Secret.value` as before — documented release limitation; consumers
  needing a closed memory window for block form should post-process
  the message (e.g. chameleon's `parse.Move` walker) or normalize
  their PXF to scalar shorthand. Hint and fingerprint metadata are
  always assigned to the proto message; they are diagnostic, not
  sensitive. Invalid UTF-8 in the value is rejected before the hook
  fires (same hardening rule as the standard string-field path).

### Coordination

- Pairs with chameleon's adoption PR closing `chameleon#7` (the
  plaintext-in-heap window meta-issue). Chameleon's `parse.Move`
  remains the canonical post-decode walker; with this hook wired,
  it walks an already-empty `Secret.value` for scalar-shorthand
  secrets and only acts on the residual block-form cases.

## [0.75.0] — 2026-05-12

Per-row binding release. Adds `TableReader.Scan(proto.Message)` and
the standalone `pxf.BindRow` helper — the convenience sugar promised
in v0.74's PR body that turns the streaming row API into a one-liner
loop over decoded proto messages. Wire format unchanged. No new
spec content; the existing §3.4.4 "Streaming consumption" framing
already permits this shape.

### Added

- **`TableReader.Scan(proto.Message)` + `pxf.BindRow` helper.** The
  per-row binding sugar promised in v0.74's PR body. Reads the next
  row via `Next()` and binds its cells to the message fields by
  column name:

  ```go
  tr, err := pxf.NewTableReader(r)
  for {
      msg := dynamicpb.NewMessage(desc)
      if err := tr.Scan(msg); errors.Is(err, io.EOF) {
          break
      }
      // msg is now populated; process it.
  }
  ```

  Cell-state semantics match the spec (§3.4.4): an empty cell leaves
  the field absent and runs the existing pxf.default / pxf.required
  pass; `null` clears wrappers / optional / oneof per §3.9; any other
  value sets the field. Enum names, RFC3339 timestamps, Go-style
  durations, and proto3 wrappers all bind correctly because the
  implementation reuses the existing Unmarshal pipeline (see
  "Implementation" below).

  `BindRow(msg, columns, row)` is also exported standalone so
  callers iterating the materializing path's `Result.Tables()[i].Rows`
  can use the same binding logic.

  Implementation: instead of growing a parallel Value-to-FieldDescriptor
  switch with ~50 arms (one per AST value type × field kind), the
  helper renders each non-nil cell back to its PXF text form
  (`<column> = <value>\n`) and runs the synthetic body through
  `UnmarshalOptions{SkipValidate: true}.Unmarshal`. That reuses every
  branch of the existing decoder — WKT timestamps and durations,
  wrapper-type nullability, enum-by-name resolution, defaults,
  required, oneof — for zero new switch arms. Cost: an extra
  format-and-reparse step per row. That's an acceptable trade for a
  streaming convenience API; consumers needing the absolute minimum
  per-row cost can keep iterating `Next()` and binding cells
  themselves.

  Tests: happy-path scan across `AllTypes`, empty-cell leaves
  field at zero, `null` clears a wrapper, WKT timestamps and
  durations bind, all scalar variants round-trip through
  format+reparse, strings with escapes preserve content, sticky
  EOF after exhaustion, `BindRow` against the materializing path,
  arity-mismatch rejection, and an equivalence check that
  streaming `Scan` and materializing `BindRow` produce identical
  wire bytes for the same input.

  Public API additions: `pxf.TableReader.Scan(proto.Message)`,
  `pxf.BindRow(proto.Message, []string, TableRow)`. Non-breaking.

## [0.74.0] — 2026-05-12

Streaming `@table` release. Adds `pxf.TableReader` over `io.Reader`
— the row-by-row API for the CSV-replacement workload that v0.73's
materializing-only path couldn't serve. Working-set memory bounded
by the size of the largest single row, not by the size of the row
sequence. Wire format unchanged. Spec-side counterpart:
[trendvidia/protowire#22] (draft §3.4.4 "Streaming consumption"
note).

### Added

- **`pxf.TableReader` — streaming `@table` consumption.** Companion to
  the upstream §3.4.4 "Streaming consumption" spec note. Reads rows
  one at a time from an `io.Reader` with working-set memory bounded
  by the size of the largest single row — not by the size of the row
  sequence. The shape consumers asked for the moment they saw the
  v0.73 materializing-only API:

  ```go
  tr, err := pxf.NewTableReader(r)        // reads through leading
                                          // directives + @table header
  cols := tr.Columns()
  for {
      row, err := tr.Next()
      if errors.Is(err, io.EOF) { break }
      if err != nil { return err }
      process(row)
  }
  ```

  Multi-table documents chain via `tr.Tail()`, which exposes any
  bytes the reader buffered but didn't consume followed by the
  remaining source. `tr.Directives()` exposes side-channel
  directives (`@<name>` / `@entry`) seen before the `@table` header,
  so consumers can attach per-table metadata via a preceding
  `@header` block (chameleon's pattern).

  Implementation: a byte-level row-boundary scanner pulls bytes from
  the underlying `io.Reader` on demand and slices one `( ... )` row
  range at a time, which is then handed to the existing
  `parser.parseTableRow` for cell decoding. Row-boundary scanning is
  string / bytes-literal / line-comment / block-comment aware so
  embedded parens don't trip the scan. Per-row arity and v1
  cell-grammar (no list / no block cells) errors surface as soon as
  the offending row is consumed — not deferred — per the spec
  requirement.

  Header parsing reuses `Parse()` against the buffered header prefix
  (everything up through the closing `)` of the column list), so the
  standalone constraint (no `@type` alongside `@table`, no body
  fields) and the dotted-column rejection get the same enforcement
  the materializing path uses. The header byte budget caps at 64 KiB
  — a fail-fast bound against a `TableReader` pointed at a giant
  body-only document with no `@table` ever.

  Tests cover the basic flow, all three cell states, side-channel
  directives before the header, sticky-error semantics, list / block
  cells rejected mid-stream, strings / triple-quoted strings / bytes
  literals / line + block comments with embedded parens or `)`,
  blank lines between rows, byte-at-a-time `io.Reader` (every
  buffer-boundary case), multi-table chaining via `Tail()`,
  equivalence with the materializing path (byte-identical cell
  type/value sequence per the spec's "MUST produce byte-identical
  row sequences" requirement), oversized-header rejection, and a
  `bytes.Buffer` smoke test.

  Public API additions: `pxf.TableReader`, `pxf.NewTableReader`,
  `pxf.ErrNoTable`. Method set: `Type()`, `Columns()`,
  `Directives()`, `Tail()`, `Next()`.

  Deferred to v0.75: `TableReader.Scan(proto.Message)` for direct
  proto binding (today consumers iterate `Next()` and bind via
  `pxf.UnmarshalDescriptor` or their own walker). The streaming
  contract is stable; the binding sugar can land non-breaking.

## [0.73.0] — 2026-05-11

Companion release to `protowire` v0.73.0. Three additive PXF text-
format changes, no wire-format impact: a schema-level reserved-name
constraint (draft §3.13), the `@entry` directive plus a generalized
zero-or-more prefix list on every named directive (§3.4.3), and the
`@table` bulk-rows directive (§3.4.4) — the protowire-native CSV.

### Added

- **Schema reserved-name check.** New `pxf.ValidateFile` /
  `pxf.ValidateDescriptor` walk a protobuf FileDescriptor and report
  every message-field, oneof, or enum-value name that case-sensitively
  collides with `null`, `true`, or `false`. Such names lex as PXF
  value keywords, so the declared element is unreachable from PXF
  surface syntax — the binding silently can't be selected. The check
  runs by default at the top of every `Unmarshal*` call and rejects
  non-conformant schemas before any decoding happens. Callers that
  have already validated their descriptors (registry-load passes,
  codegen pre-screening) can set `UnmarshalOptions.SkipValidate = true`
  to bypass the per-call recheck.

- **`@entry` directive + zero-or-more prefix list.** `named_directive`
  now accepts `*( IDENT )` between `@<name>` and the optional
  `{ ... }` block (was `[ IDENT ]`). The grammar is whitespace-
  insignificant, so the parser uses one-token lookahead to keep a
  body field key from being eaten as a directive prefix (an IDENT
  followed by `=` or `:` is a body entry, not a prefix). The
  `pxf.Directive` AST grows a `Prefixes []string` field exposing the
  full prefix sequence; the legacy `Type` field is preserved
  (populated from `Prefixes[0]` when there's exactly one prefix) so
  v0.72.0-era consumers like chameleon's `@header` reader keep
  working unchanged.

  `@entry` is consumer-interpreted; the parser records the prefixes
  and body but assigns no meaning. The dot-disambiguation rule
  ("single dotted prefix ⇒ type; single bare prefix ⇒ label") for
  the one-prefix form is a semantic convention applied by the
  consumer, not the parser.

- **`@table` directive.** New top-level form:

  ```
  @table <type> ( col1, col2, ... )
  ( val1, val2, ... )
  ( val1, val2, ... )
  ```

  Lexer additions: `LPAREN` / `RPAREN` tokens, a dedicated `AT_TABLE`
  keyword, and a one-character fix to the timestamp lexer so values
  like `2026-05-11T10:00:00Z` don't eat their row's closing `)`.

  AST: new `pxf.TableDirective` (Type, Columns, Rows) and
  `pxf.TableRow` (Cells). `Document` grows a `Tables []TableDirective`
  field. Three-state cells: a `nil` Value in `TableRow.Cells` is an
  empty cell (absent field); a `*NullVal` is present-but-null; any
  other Value is present-with-value — same semantics as the keyed
  form, just spelled positionally.

  v1 restrictions (intentional, relaxable later): cells are scalar-
  shaped (no `[...]` lists, no `{...}` blocks); columns are
  unqualified field names (no dotted paths); rows have strict arity
  (row arity MUST equal column count); a document with `@table`
  MUST NOT carry `@type` or top-level field entries (the `@table`
  header IS the document's type declaration). All five rules are
  enforced by both `Parse` and the direct-decode path; error
  messages cite draft §3.4.4.

  Tables flow through `UnmarshalFull` via `Result.Tables()`. Plain
  `Unmarshal` silently discards table data (the bound message stays
  zero-valued, since the document has no body) but still enforces
  the standalone constraint.

### Changed

- `UnmarshalOptions` grows `SkipValidate bool` (default false). The
  default-on behavior is the safe one because reserved-name traps
  are silent; pre-validating callers opt in to the skip.

- `pxf.Directive.Type` is now derived from `Prefixes` (back-compat):
  populated when `len(Prefixes) == 1`, empty otherwise. New code
  should read `Prefixes` directly.

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

[trendvidia/protowire#116]: https://github.com/trendvidia/protowire/issues/116

[Unreleased]: https://github.com/trendvidia/protowire-go/compare/v1.2.2...HEAD
[1.2.2]: https://github.com/trendvidia/protowire-go/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/trendvidia/protowire-go/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/trendvidia/protowire-go/compare/v1.1.2...v1.2.0
[1.1.2]: https://github.com/trendvidia/protowire-go/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/trendvidia/protowire-go/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/trendvidia/protowire-go/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/trendvidia/protowire-go/compare/v0.77.0...v1.0.0
[0.77.0]: https://github.com/trendvidia/protowire-go/compare/v0.76.0...v0.77.0
[0.76.0]: https://github.com/trendvidia/protowire-go/compare/v0.75.0...v0.76.0
[0.75.0]: https://github.com/trendvidia/protowire-go/compare/v0.74.0...v0.75.0
[0.74.0]: https://github.com/trendvidia/protowire-go/compare/v0.73.0...v0.74.0
[0.73.0]: https://github.com/trendvidia/protowire-go/compare/v0.72.0...v0.73.0
[0.72.0]: https://github.com/trendvidia/protowire-go/compare/v0.71.0...v0.72.0
[0.71.0]: https://github.com/trendvidia/protowire-go/compare/v0.70.3...v0.71.0
[0.70.3]: https://github.com/trendvidia/protowire-go/compare/v0.70.2...v0.70.3
[0.70.2]: https://github.com/trendvidia/protowire-go/compare/v0.70.1...v0.70.2
[0.70.1]: https://github.com/trendvidia/protowire-go/compare/v0.70.0...v0.70.1
[0.70.0]: https://github.com/trendvidia/protowire-go/releases/tag/v0.70.0
