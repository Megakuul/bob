# Out of scope for v1

This list comes from REQUIREMENTS §23 plus later owner decisions. Stage 3 agents must not build
any of these. If a task seems to need one, stop and report it.

| Item | Why / where decided | Instead |
|---|---|---|
| Source hosting and versioning | §23: a forge sits in front | Forge adapters for GitHub, Forgejo and GitLab (§16) |
| Deploying built software; infrastructure secrets management | §23 | Releases, OCI images and AppImages as outputs |
| Multi-tenancy | §23 | One installation per organisation; RBAC scoped to RPCs |
| darwin, Windows, BSD | §23 | Linux x86_64 and arm64 only |
| Developer pushes to the shared cache | §23; confirmed by S13 (a rootless store can be tampered with by builds) | `--remote` builds on the server |
| `bob add` and other code-rewriting commands | §23 | Manual edits to `bob.go` |
| Raw Nix escape hatch | §23 | Typed ecosystem APIs; nixpkgs packages by attribute |
| Impure pipeline steps | §23 (roadmap evaluation) | Pure derivations; loopback services in the sandbox (R-FW-025) |
| Worker provisioning and autoscaling | §23 | Workers register themselves through the fleet bucket |
| Component libraries in the UI | §23 | Tailwind with hand-written components |
| Minting S3 credentials (STS, per-worker S3 users) | D57: S3 credentials are a deployment concern | Workers get S3 credentials from their environment (e.g. IMDSv2) |
| Presigned S3 download URLs | D61 | Streaming download proxy in the orchestrator |
| S3 backends without full conditional writes (SeaweedFS, Garage) | D63 | AWS S3 and compatible servers; RustFS is the reference test backend |
| LIST / dynamitedb `Query` on hot paths | D60 | GET and conditional PUT on known keys |
| Continuous worker→orchestrator log streaming; orchestrator↔orchestrator log peering | D55 | Batched lake writes plus an on-demand pull of in-memory logs |
| Builds running as root | D56 | Root worker, non-root isolated builds |
| Lix | D50 | Embedded static Nix |
