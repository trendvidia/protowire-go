<!--
Thanks for contributing! Fill out the sections below — the review system
checks for these and your PR will get faster feedback when they are
present.

For wire-format changes (PXF grammar, SBE schema interpretation,
envelope semantics), please open the corresponding PR in
trendvidia/protowire first so every language port stays compatible.
-->

## Summary

<!-- One or two sentences on what this PR does and why. -->

## Type of change

- [ ] Bug fix
- [ ] New feature / API addition
- [ ] Performance improvement
- [ ] Refactor (no behavior change)
- [ ] Documentation
- [ ] Security hardening

## Test plan

<!-- How did you verify the change? Unit tests, fuzz seeds, manual
     repro, benchmarks. List concrete commands the reviewer can run. -->

- [ ] `go test -race -count=1 ./...`
- [ ] `go vet ./...`
- [ ] `gofmt -s -l .` clean
- [ ] New tests cover the change (if behavior change)

## Benchmarks (if hot-path change)

<!-- Include before/after `go test -bench=.` numbers for the touched
     package. Skip this section if the change does not touch
     encode/decode paths. -->

```text
before:
after:
```

## Wire-format / spec impact

<!-- Tick the one that applies. -->

- [ ] No wire-format change
- [ ] Wire-format change — corresponding spec PR: <link>

## Checklist

- [ ] PR is scoped to one concern
- [ ] Public API additions are documented (godoc)
- [ ] CHANGELOG.md updated under `[Unreleased]` if user-visible
- [ ] No new third-party dependencies (or justified in summary)
