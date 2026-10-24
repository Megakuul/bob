---
title: Specification
section: Reference
order: 1
summary: Where the exact specification, design decisions and harness documentation of bob live in the repository.
---

The documentation explains how to use bob. The exact, testable behaviour is written down in
the repository. This page points you to it.

## Documents

| File | Content |
|---|---|
| `SPEC.md` | the complete specification, generated from the chapters below; every requirement has an id (`R-CLI-001`) and acceptance criteria |
| `spec/01-core.md` | `bob.go` rules, labels, the core API, `bob.lock`, the Nix layer, the embedded store |
| `spec/02-cli-dev.md` | commands, exit codes, the error table, JSON output, dev environments, login and installs |
| `spec/03-ecosystems.md` | Go, Rust, C/C++, npm, OCaml and the code generators |
| `spec/04-outputs-platforms.md` | platforms, variants, cross-compiling, OCI images, tarballs, AppImages, releases |
| `spec/05-server-data.md` | buckets, policies, records, state machines, workers, logs, configuration |
| `spec/06-server-api.md` | the HTTP API, identity, RBAC, cache, registry, releases, forges, the web UI |
| `docs/adr/` | the design decisions (ADRs), for example 0001 (how bob writes derivations) and 0002 (the rootless store) |
| `docs/operations/worker-compromise.md` | the runbook for a compromised worker |
| `HARNESS.md` | how bob is tested: the CI command, fixtures, goldens, budgets, and the documentation guardrail |
| `proto/` | the API definitions (protobuf) |
| `schema/events-v1.json` | the JSON schema of `--json` output |

When the documentation and the specification disagree, the specification describes the
intended behaviour and the [status page](03-status.md) says what is not there yet.

## Next steps

- [API index](02-api-index.md).
- [Limitations and status](03-status.md).
