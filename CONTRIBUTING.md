# Contributing to protowire-go

Thanks for your interest in contributing! This repository is the Go
reference port of the [PXF text format](https://github.com/trendvidia/protowire),
the schema-free `pb` marshaler, and the SBE codec. It is part of the
`protowire-*` family of cross-language ports.

## Quick Start

```bash
git clone https://github.com/trendvidia/protowire-go.git
cd protowire-go
go test ./...        # full test suite, ~2s
go vet ./...         # static checks
go test -bench=. ./encoding/pxf ./encoding/sbe   # benchmarks
```

The module depends on a small fork of `google.golang.org/protobuf` that
adds `dynamicpb.SetUnsafe` / `AppendUnsafe` / `MapSetUnsafe`. The fork
is pulled in via a `replace` directive in `go.mod` and points at the
public release at
[`github.com/trendvidia/protobuf-go`](https://github.com/trendvidia/protobuf-go).
You do not need a local checkout.

## What Makes a Good PR

- **One concern per PR.** Bug fix, feature, or refactor — pick one.
- **Tests first, or tests with the change.** New decoder paths need
  positive, negative, and adversarial-input cases.
- **No regressions on the benchmark suite.** If your change touches a
  hot path, include before/after `go test -bench=.` numbers in the PR
  body.
- **Wire-format changes go through the spec repo.** PXF grammar, SBE
  schema interpretation, and envelope semantics are defined in
  [`trendvidia/protowire`](https://github.com/trendvidia/protowire) so
  every port stays compatible. Open a spec PR there *first*, then port
  the change here.

## Code Style

- Run `gofmt -s` before committing — CI will reject otherwise.
- Public API documentation goes on every exported symbol, in full
  sentences, leading with the symbol name.
- Prefer returning `error` over `panic` on the public API surface.
  Panics are reserved for `View`-style programmer-error contracts and
  must be documented at the package level.
- Avoid new dependencies. The current go.mod is intentionally small; a
  PR that adds a third-party module will be asked to justify it.

## Reporting Bugs

Open a GitHub issue with a reproducer (Go test, raw payload bytes
hex-encoded, or both). Please state the commit / tag you observed the
bug on. For security-sensitive reports, see [`SECURITY.md`](SECURITY.md)
instead — those should never go through public issues.

## Code of Conduct

Participation is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

---

# Governance Addendum: Steward

This repository is managed by **Steward** — an autonomous AI governance
agent that reviews, scores, and merges pull requests. There are no
human gatekeepers in the merge path. Code is evaluated, scored, and
merged based on objective, deterministic rules. The public surface of
Steward lives at <https://steward-dev.ai>.

## 1. How Code Gets Merged (the Lifecycle)

1. **You open a Pull Request.**
2. **Steward evaluates it.** Steward runs static analysis, evaluates
   cyclomatic complexity, checks dependency licenses, and calculates
   the required reputation threshold based on the files you touched.
3. **Steward outputs a Diagnostic Log.** Within seconds, Steward
   comments on your PR with a detailed breakdown of its mathematical
   evaluation.
4. **Community Voting (if required).** If your code passes all security
   and quality gates, Steward opens the PR for weighted voting.
5. **Auto-Merge.** If the required consensus threshold is met, Steward
   merges the code automatically.

## 2. The Escrow Pipeline (For New Contributors)

If this is your first time contributing, you currently have a
Reputation Vector of `0`.

**Do not submit massive, architectural rewrites as your first PR.** If
you submit a PR that modifies core infrastructure and you have `0`
reputation, Steward will automatically place your PR into **Escrow
Status**.

**How to unlock Escrow:**

1. Steward will automatically generate and assign you 2–3 isolated
   issues labeled `sandbox`.
2. These are safe, low-blast-radius tasks (improving test coverage,
   documentation, etc.).
3. Once you successfully merge the `sandbox` issues, your reputation
   vector increases, and Steward will automatically unlock your
   original PR for the community vote.

## 3. Private Mentorship Mode

If you want Steward to evaluate your code *without* posting a public
review comment on the PR timeline, use **Draft Mode**.

- Open your Pull Request as a **Draft**.
- Steward will evaluate your code and send its feedback as a private
  review visible only to you.
- Use this mode to iterate on performance bottlenecks or style
  violations privately. Once the math looks good, mark the PR as
  **Ready for Review** to trigger the public evaluation.

## 4. How to Read a Rejection Log

Steward does not reject code based on opinions. If your PR is blocked,
the diagnostic log will state the mathematical or constitutional
reason. Example:

> **Action: Blocked**
> - **Reason:** Cyclomatic complexity threshold exceeded.
> - **Details:** `encoding/pxf/parser.go` introduced a nested loop with
>   a complexity score of 12. The Constitution limits this domain to a
>   maximum score of 8.
> - **Resolution:** Refactor the parsing logic to flatten the condition
>   tree before requesting a re-evaluation.

Do not argue with Steward in the comments. Refactor the code to
satisfy the metric, push the commit, and Steward will automatically
re-evaluate.

## 5. Useful Commands

You can interact with Steward by commenting on issues or PRs:

- `/steward evaluate` — Forces Steward to immediately re-run the
  9-dimension check on your latest commit.
- `/steward check-reputation` — Steward replies with your current
  decayed reputation vectors and your effective voting weight for the
  current PR's domain.
- `/steward sandbox-me` — Type this on any open issue, and Steward
  will find an unassigned, low-risk issue and assign it to you to
  help build your initial reputation.

## 6. Reporting Bugs Through Steward

If Steward verifies that a bug was introduced in a previously merged
PR, it will retroactively slash the original author's reputation
vector. Please write thorough unit tests.
