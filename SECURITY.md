# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `protowire-go`, please report it
privately. **Do not open a public GitHub issue or pull request** for
security-sensitive reports.

Preferred channels:

1. **GitHub Private Vulnerability Reporting** — open a report at
   <https://github.com/trendvidia/protowire-go/security/advisories/new>.
   This routes directly to the maintainers and keeps the disclosure private
   until a fix is published.
2. **Email** — `security@trendvidia.com`. PGP key available on request.

Please include:

- A description of the issue and the impact you observed.
- Steps to reproduce, ideally a minimal payload or test case.
- The commit or release tag the report applies to.
- Whether you intend to publish a writeup, and on what timeline.

## What to Expect

- Acknowledgement within **3 business days**.
- An initial assessment (severity, scope, affected versions) within **7
  business days**.
- Status updates at least every 14 days until the report is closed.
- Credit in the release notes and any published advisory, unless you ask
  to remain anonymous.

## Scope

In scope:

- The `encoding/pb`, `encoding/pxf`, `encoding/sbe`, and `envelope`
  packages in this repository.
- Memory-safety, panics on attacker-controlled input, integer overflow,
  out-of-bounds reads, and parser-confusion issues in any decoder.

Out of scope:

- Vulnerabilities in third-party dependencies — please report those
  upstream. We will of course bump dependency versions in response.
- Issues that require a malicious *trusted* schema (i.e. a hostile
  `.proto` or SBE XML supplied by the operator). Schemas in this codec
  are part of the trust boundary; only the wire payload is treated as
  untrusted.
- Performance regressions, denial-of-service through legitimate but
  expensive payloads (these are tracked as normal issues).

## Supported Versions

Until `v1.0.0`, only the latest minor release receives security fixes.
After `v1.0.0`, the policy will be revisited and documented here.

## Coordinated Disclosure

We follow a 90-day coordinated disclosure window by default. If a fix
ships sooner, the advisory will be published immediately. Reports
involving an actively exploited vulnerability will be triaged
out-of-band — flag this in your initial email.
