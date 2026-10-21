# ADR 0015 — Worker isolation: evaluator sandbox and build hardening

- **Status:** Accepted (Gate S pending), 2026-10-21 (S13, 2026-10-20; decision 4 rewritten by D56)
- **Requirements:** R-FLEET-008, R-SEC-000, R-SEC-001, R-SEC-004, R-FW-008, R-FW-025
- **Evidence:** `docs/spikes/s13-sandbox.md`, code in `spikes/s13-sandbox/`

## Context

- `bob.go` is arbitrary Go and runs on workers.
- ADR 0002 makes Nix rootless. In that mode a build runs with the same kernel uid that owns the
  store and the worker's credentials.

S13 measured:
- **Rootless builds can rewrite input store paths persistently.**
- FODs get the host network, including host loopback and abstract sockets.
- Loopback inside normal builds works for integration-test services.

## Decision

1. **Evaluate `bob.go` in a rootless, bob-built sandbox (no external tools):**
   - a delegated cgroup v2 leaf (memory, pids, cpu, wall clock via `cgroup.kill`);
   - user, mount, net, pid, ipc, uts and cgroup namespaces;
   - a tmpfs root with only read-only binds;
   - a nested userns (uid 1000, no capabilities);
   - no_new_privs, Landlock (fs, TCP, scopes) and a pure-Go seccomp deny-list;
   - graph IR on fd 3 with a byte cap.

   Workers refuse to start without cgroup delegation or Landlock. Local evaluations use the same
   sandbox; limits apply only where delegation exists.
2. **The Go build cache** is a trusted seed (overlay lower layer) plus a throwaway upper layer per
   evaluation.
3. **IR → derivation is trusted worker code.** User-declared sources are fetched by bob (egress
   guard, hash check) and imported with `nix store add`, which gives the same path as the FOD.
   Workers never build user-influenced FODs.
4. **(Rewritten, D56.) Root worker, non-root isolated builds.**
   - The worker runs as root and refuses to start otherwise.
   - Each job runs in a job container built by bob: a cgroup leaf; mount, pid, ipc, uts and
     cgroup namespaces; a private `/tmp`; the persistent root-owned store bound at `/nix`; no
     worker credentials.
   - Nix runs as root inside the container with `sandbox = true`, `auto-allocate-uids = true` and
     `use-cgroups = true`, so every build runs as an allocated non-root uid.
   - The rootless overlay mode is dropped, which removes open risks 1 and 2.
   - evalbox maps its uid to a dedicated unprivileged host uid.

   Exact contract: SPEC R-FLEET-008.
5. **Test services (R-FW-025)** run inside the test derivation's own netns on loopback. Each
   service gets its own `127.0.0.N` with its default port. Teardown comes from the pid namespace.
   API: `Services: []bob.Service{postgres.Service(…), bob.ServiceFrom(…)}`.

## Consequences

- About 20 ms per evaluation for the sandbox. A seeded evaluation takes about 0.6 s; a cold one
  13.5 s [8c/15G].
- Postgres tests cost about 1.5 s per test derivation.
- Workers need systemd `Delegate=yes` (or an equivalent delegated cgroup) and kernel Landlock
  ABI ≥ 4.
- The backend-B fetch path changes: bob downloads, Nix only verifies the path.

## Rejected

- bubblewrap, runc, gVisor or a Docker daemon: extra executables and daemons (D6, principle 6).
- Seccomp through libseccomp: needs cgo.
- Evaluating without a sandbox on workers: violates R-FLEET-008.
- Relying on Nix's sandbox alone on rootless workers: store tampering was measured.

## Open risks

See write-up §6:
- overlayfs lower-layer changes while mounted;
- db copy-up on large stores;
- nested userns allowed inside Nix builds;
- amd64 seccomp never run;
- Landlock availability across distros.

## Owner amendments

- **D56**: decision 4 rewritten; OD-S13-1 resolved as a root worker, and the rootless overlay
  mode of options a/c is removed. The rejected list is unchanged: the job container is bob's own
  namespace code, not runc, bubblewrap or Docker.
- Open risk 3 (nested user namespaces inside builds) stays open (SPEC §05.14).
