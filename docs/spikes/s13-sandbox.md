# S13 — Worker isolation: sandboxed `bob.go` evaluation, Nix build sandbox, loopback services

Status: **done (indicative numbers)**, 2026-10-20, host `vibe-engine` (see `docs/environment.md`).
Code: `spikes/s13-sandbox/` (throwaway). ADR: `docs/adr/0015-worker-isolation.md` (Proposed).
Other spikes were running at the same time, so every timing is indicative. `measure.sh` reruns them all.

## 1. Question and requirements

1. How does a worker evaluate an untrusted `bob.go` safely? (R-FLEET-008, R-SEC-000, R-SEC-001,
   R-SEC-004, R-FW-008)
2. What does the Nix sandbox isolate when it runs rootless (ADR 0002), and should workers use
   build users when they have root? (R-SEC-000)
3. Can a derivation start loopback services for integration tests? (R-FW-025) And which
   integration tests can run fully offline?

## 2. What was built

| Path | What |
|---|---|
| `cmd/evalbox/` | Rootless evaluator sandbox. Go, `CGO_ENABLED=0`, only dependency `golang.org/x/sys`. Parent, stage1 and stage2 (see §5.2). |
| `cmd/evalbox/seccomp.go` | Seccomp deny-list written as classic BPF in pure Go (no libseccomp). |
| `cmd/evalbox/landlock.go` | Landlock filesystem rules, a TCP deny rule and scopes. Raw syscalls; runs on ABI 9 here. |
| `cmd/evalbox/cgroup.go` | Delegated cgroup v2 leaf per evaluation, entered with `clone3(CLONE_INTO_CGROUP)`. Uses `cgroup.kill` and the events files. |
| `testdata/evil/` | Hostile "bob.go" with 15 cases: secrets, network, escape, 3 kinds of fork/thread bomb, memory bomb, CPU loop, disk fill, oversized output. |
| `cmd/bait/` | Host-side bait: listeners on TCP 127.0.0.1:21300, abstract `@s13-bait` and a unix socket path. |
| `cmd/loopsvc/` | Tiny HTTP service and probe client for the loopback tests. |
| `nix/*.nix` | Spike-only probe derivations built with `builtins.derivation`: `probe.nix`, `storewrite*.nix`, `services.nix`, `fodeq.nix`. |
| `run-part1.sh`, `nixs13.sh`, `nix/services.sh`, `nix/overlay.sh`, `measure.sh` | Runners. Logs are in `tmp/`. |

Setup:
- Part 1 runs as `bobtest` (uid 1000) inside `systemd-run -p User=bobtest -p Delegate=yes`. This
  models a worker unit with `User=bob-worker, Delegate=yes`. Nothing in the evaluation path runs
  as root.
- The Nix tests run rootless through the S0b `bobns` with a separate state,
  `/home/bobtest/.local/share/bob-s13`.
- For comparison, some tests also run with root Nix: build users, and `auto-allocate-uids` in a
  scratch store (`/root/scratch-s13/rootstore`).

## 3. Results

### 3.1 Evaluator sandbox: hostile cases (`tmp/part1.log`, [8c/15G])

| Case | Result | bob error / evidence |
|---|---|---|
| Read worker credentials `~/.config/bob-worker/credentials` | contained | ENOENT: the file is not in the mount namespace |
| Read `/root`, `/etc/shadow`, host `/etc/hostname` | contained | ENOENT |
| Worker environment (`AWS_*`, `BOB_WORKER_*` set in the parent) | contained | the evaluator sees only the 13 variables bob passes |
| `/proc/1/environ`, host pids | contained | fresh procfs with `hidepid=2`; one pid visible |
| `/run/user/1000`, `/sys` | contained | not mounted |
| TCP to 1.1.1.1:443, host loopback :21300, 169.254.169.254 | contained | `EACCES` from Landlock. Without Landlock: `ENETUNREACH` (private netns, `lo` down). The bait saw no connection. |
| DNS | contained | no resolver reachable |
| Host abstract socket `@s13-bait`, host unix socket path | contained | refused (netns) / ENOENT |
| `AF_VSOCK` socket (vsock is not network-namespaced) | contained | `EAFNOSUPPORT` (seccomp) |
| `ptrace(1)`, `mount`, `unshare(CLONE_NEWUSER)`, child in a new userns | contained | EPERM (seccomp). Without seccomp, the new userns still fails with ENOSPC (`max_user_namespaces=1`). |
| chmod or write of a Go toolchain store file (owned by the same kernel uid!) | contained | EROFS / EACCES: read-only bind mount |
| Write to `/src` | contained | EROFS |
| `/proc/1/exe` open for write / readlink | contained | hidden (`hidepid=2`; stage1 is non-dumpable) |
| `io_uring_setup`, `bpf` | contained | EPERM (seccomp). Without seccomp these calls reach the kernel (EFAULT/EINVAL), which shows the attack surface seccomp removes. |
| Evaluator credentials | — | `uid=1000 CapEff=0 NoNewPrivs=1 Seccomp=2` |
| Fork bomb, Go self-exec (`-pids 128`) | stopped, 0.6 s | `✗ evaluating bob.go failed after trying to start more than 128 processes or threads (exit 1)` |
| Fork bomb, bash | contained, host unaffected; 935 `pids.max` hits | same message (exit 142) |
| Thread bomb (LockOSThread goroutines) | stopped, 1.4 s | same message (exit 2) |
| Memory bomb (`-mem 512M`) | OOM in 0.9 s | `✗ evaluating bob.go used more than 512M of memory and was stopped (peak 512 MiB)` |
| CPU loop (`-timeout 5s`) | killed at 5.0 s via `cgroup.kill` | `✗ evaluating bob.go took longer than 5s and was stopped` |
| Disk fill (`-work 256m`) | ENOSPC after 252 MiB | `✗ evaluating bob.go failed (exit 1)`. **tmpfs pages count toward memory.max** (`mem_peak` 274 MB). |
| 200 MiB written to the graph fd (`-outmax 16M`) | killed in 0.6 s | `✗ evaluating bob.go produced more than 16 MiB of graph output and was stopped` |
| Cleanup | ok | 0 `eval-*` cgroup leaves left; nothing left in the unit |

**Tests with one layer removed:**
- Without Landlock, network and secrets are still contained, by the namespaces.
- Without seccomp, the escape cases are still contained, by the namespaces, the read-only mounts
  and the userns cap.

So no single layer is load-bearing alone, except the read-only binds. The next table shows why
those matter.

### 3.2 Evaluator timings (`tmp/cold.log`, `tmp/overhead.log`)

| | [8c/15G] | [2c/4G] |
|---|---|---|
| `bash -c true` direct | 4.9 ms | 4.1 ms |
| same through evalbox, no cgroup | 20.3 ms | 17.5 ms |
| same through evalbox + cgroup leaf | 25.0 ms | 19.5 ms |
| stage1 ready / exec evaluator (self-reported) | 10–18 ms / 13–26 ms | n/m |
| Evaluation `go build` + run, **cold GOCACHE** | 13.5 s, 319 MB peak, 69 pids peak | 18.8 s, 330 MB, 20 pids |
| same with **trusted seed GOCACHE** as overlay lower layer (46 MB) | 0.56 s, 49 MB, 26 pids | 0.57 s, 50 MB, 14 pids |
| evalbox nested inside bob's rootless namespace (bobns) | works (stage1 9 ms) | n/m |

These numbers are median of 21 runs (overhead) or single runs (builds). They depend on machine
load, so rerun `measure.sh` on a quiet machine.

### 3.3 Nix sandbox, rootless, no build users (`tmp/nix-probe.log`)

| Property | Normal derivation | Fixed-output derivation (FOD) |
|---|---|---|
| uid / caps / seccomp | 1000 (`nixbld`), CapEff 0, NoNewPrivs 1, Nix seccomp filter | same |
| pid ns, `/proc` | private; only own pids | same |
| Visible fs | `/bin /build /dev /etc /nix /proc /tmp`; only the input closure (14 paths); no `/home`, no `/sys` | same, plus host `/etc/hosts`, `resolv.conf`, `services`, `ssl` |
| Network | private netns with `lo` 127.0.0.1/8 and ::1 | **host network**: `eth0`, internet OPEN, **host loopback 127.0.0.1:21300 OPEN** (the bait logged the connection), **host abstract unix sockets visible** (`@s13-bait` in `/proc/net/unix`) |
| setuid bit, `mount`, `/proc/sys` write, `dmesg` | denied | denied |
| Nested user namespace (`unshare -U`) | **allowed** | **allowed** |
| Per-build cgroup | no (build stays in the caller's cgroup); `use-cgroups=true` works rootless in a delegated cgroup (cgroup ns root `/`) | same |
| Process left running when the builder exits | killed (pid ns ends); 0 left on the host | same |
| **Modify an input store path** (`storewrite-fixed.nix`) | **CHMOD-OK, WRITE-OK; the real store file changed persistently** (`original\npoisoned` in `~/.local/share/bob-s13/nix/store/…-s13-victim`) | same |

Comparison with root Nix:

| | rootless (ADR 0002) | root Nix + build users (`nixbld`) | root Nix + `auto-allocate-uids` |
|---|---|---|---|
| Input path chmod/write | **succeeds** (builder kuid = store owner kuid) | denied (EPERM / EACCES; store owned by root, shown as `nobody`) | denied |
| Host-side uid of the builder | the worker's own uid | `nixbld-N` | 872415232 (allocated range; no passwd entries needed) |
| A sandbox escape leads to | the worker's uid (credentials readable) | an unprivileged build uid | same as build users |

**Mitigation tested for rootless (`nix/overlay.sh`):**
- Inside bob's namespace, mount an overlay over `/nix` for each job: the persistent `/nix` as the
  lower layer, a throwaway upper layer.
- The poisoned write then lands only in the upper layer. The persistent store still reads
  `original`.
- Cost: the overlay mount took 8.9 ms. Nix's db (8.2 MB here) is copied up on first write; the
  upper layer was 236 KB after the job. [8c/15G]
- Not measured: a large store db (copy-up cost), promoting outputs back to the persistent store,
  and changes to the lower layer while overlays are mounted.

**FOD avoidance (`fodeq.nix`):**
- `nix store add --mode flat --hash-algo sha256 --name X` gives exactly the output path of a
  `builtin:fetchurl` FOD with the same name and hash (`/nix/store/35vl72…-s13fodeq.txt`).
- Once the path exists, `nix build` of that FOD succeeds with an unreachable URL.
- So bob can fetch sources itself and import them. Nix then never runs a user-influenced FOD.

### 3.4 Loopback services inside the rootless sandbox (`tmp/services-rootless*.log`)

| Test | [8c/15G] | [2c/4G] |
|---|---|---|
| Postgres 17.11: `initdb` (no-sync) | 0.8–1.3 s | 0.9–1.2 s |
| → `pg_ctl -w start` on 127.0.0.1:5432 | +0.12 s | +0.12 s |
| → 100k-row insert + count via TCP (`psql -h 127.0.0.1`, and `localhost` → 127.0.0.1) | +0.10 s | +0.10 s |
| → `pg_ctl stop -m fast` | +0.12 s | +0.13 s |
| Whole `nix build` of the test derivation | 1.3–2.0 s | 1.65–1.75 s |
| Two Postgres builds in parallel, **both on port 5432** | both ok, 1.54 s | both ok, 1.77 s |
| Postgres left running when the builder exits | build finishes (1.3 s); 0 postgres processes left on the host | same |
| Go HTTP server on 127.0.0.1, **127.0.0.2**, **[::1]**: ready after / per request (1000 requests) | 2.9 ms / 197–282 µs | 0.4–2.9 ms / 169–232 µs |
| Concurrent builds A and B: B probes A's 127.0.0.1:5000 and looks at A's pids | B cannot reach A; B sees only its own pids | n/m |

## 4. Answer

1. **`bob.go` evaluation on workers: run it in the evalbox design, entirely rootless.** The layers:
   - a delegated cgroup v2 leaf;
   - user, mount, net, pid, ipc, uts and cgroup namespaces;
   - a tmpfs root containing only read-only toolchain, source and module-cache binds;
   - a nested user namespace, so the evaluator is uid 1000 with no capabilities;
   - no_new_privs, Landlock and seccomp.

   All 15 hostile cases fail cleanly with a bob error. Overhead is about 20 ms. A trusted seed
   GOCACHE as an overlay lower layer brings an evaluation from 13.5 s down to 0.56 s, and one
   evaluation can't poison another's cache.
2. **The rootless Nix sandbox isolates network, processes and the filesystem view, but not the
   store.**
   - Any build can rewrite its input paths in the real store. On a worker that means one job can
     poison every later job and its cache uploads. That breaks R-SEC-000 and principle 1.
   - FODs get the host network, including the worker's loopback services, abstract sockets and
     cloud metadata.
   - Fix for the store: a per-job overlay `/nix` when rootless (tested), or build users when root
     is available (tested: `auto-allocate-uids` needs no passwd setup).
   - Fix for FODs: bob fetches user-declared sources itself with an egress guard and imports them,
     so workers never run user-influenced FODs.
3. **R-FW-025 works.** The sandbox has a private `lo` (127.0.0.0/8 and ::1). Postgres is ready in
   about 1.1 s and the full test takes about 1.5 s. Each build has its own netns, so the same port
   in parallel builds is fine. Teardown is automatic (pid ns). Answer to the owner's question:
   integration tests need a **network stack, not the network**. Clients talk to the service under
   test over TCP. Loopback gives exactly that and nothing outside.

### Which integration tests run offline in the sandbox

| Runs fully offline (loopback only) | Can't, and why |
|---|---|
| Databases from nixpkgs: Postgres (verified), MySQL/MariaDB, Redis/Valkey, SQLite, etcd (unverified except Postgres) | Tests against SaaS or cloud APIs (Stripe, real S3/AWS, GitHub): external network + secrets → D29 / R-FW-042 (impure steps, roadmap) |
| Services built in the same repo (Go/Rust binaries started as services; verified with a Go HTTP server) | Tests that use Docker/testcontainers: no container daemon in the sandbox. Replace with nixpkgs services. |
| Brokers and stores from nixpkgs: NATS, Kafka/Redpanda, MinIO/Garage, RabbitMQ (unverified) | Tests that download fixtures or models at run time: make them declared inputs (fetched by bob, hashed) |
| `httptest`/in-process servers, gRPC over loopback, multi-node clusters on 127.0.0.2… (verified that extra loopback IPs work) | Tests that need real DNS names, public TLS certificates or external IdPs: use local CA + `/etc/hosts`-style names (only `localhost` exists in the sandbox) |
| Headless browser tests with nixpkgs Chromium/Firefox + Playwright against a local server (unverified) | Hardware: GPU, KVM (`/dev/kvm` is not in the sandbox; NixOS-test-style `kvm` system feature is a later topic) |
| | Privileged network tests (iptables, raw sockets, tun/tap): no capabilities in the sandbox |

## 5. SPEC input

### 5.1 Worker isolation layers [proposal]

| Threat | Layer | Test (spike evidence; becomes a protected test) |
|---|---|---|
| Evaluator reads worker credentials or files | mount ns + tmpfs root with only ro binds; Landlock fs allow-list | `secrets` case: ENOENT for creds, `/root`, `/etc/shadow`, `/proc/1/environ` |
| Evaluator sees worker env | explicit env only (`cmd.Env` = list from bob) | `env has worker secrets` |
| Evaluator uses the network (exfiltration, SSRF, metadata) | net ns without interfaces up; Landlock TCP deny; seccomp socket families | `network` case + bait log shows 0 connections |
| Evaluator attacks the kernel | nested userns (uid 1000, CapEff 0), no_new_privs, seccomp deny-list, `max_user_namespaces=1` | `escape` case; layer-removal runs |
| Evaluator modifies the toolchain/store (same kernel uid in rootless) | **read-only bind mounts** (load-bearing) | `chmod store file` = EROFS |
| Fork/thread bomb | `pids.max` | 3 bomb cases → `✗ … more than N processes or threads` |
| Memory bomb / tmpfs fill | `memory.max`, `memory.swap.max=0`, `memory.oom.group=1`; `/work` tmpfs size | `membomb` → `✗ … used more than …`; `diskfill` → ENOSPC |
| Endless evaluation | wall timer → `cgroup.kill`; `cpu.max` | `cpuloop` → `✗ … took longer than …` |
| Oversized or hostile graph output | fd 3 with byte cap → kill; output is IR JSON parsed with limits (BOOTSTRAP §5.4) | `bigoutput` → `✗ … more than … of graph output` |
| Evaluation poisons the shared Go build cache | trusted seed GOCACHE as overlay **lower**, per-evaluation tmpfs upper | seeded runs; upper layer discarded |
| Build rewrites input store paths (rootless) | per-job overlay `/nix` (rootless) or build users (root) | `storewrite-fixed.nix`: persistent store unchanged |
| User-influenced FOD reaches host network / loopback / metadata | bob fetches sources itself (egress guard) and imports with `nix store add`; IR → drv is trusted code, so user code can't emit FODs | `fodeq.nix` path equality; FOD probe (shows the risk) |
| Build reaches another build | Nix per-build pid + net ns | `isoA`/`isoB` |
| Leftover processes after a build/test | pid ns teardown; job cgroup kill | `pgLeft` → 0 left |
| Sandbox escape (kernel bug) | rootless: lands as the worker uid → R-SEC-001 bucket split limits damage. root mode: lands as an allocated uid | not testable; accepted residual risk |

### 5.2 Evaluator sandbox contract [proposal]

- **Input:**
  - The source tree, ro at `/src`.
  - The Go toolchain closure, ro at its store paths.
  - A pre-downloaded module cache, ro at `/gomod`. It is filled outside the sandbox by
    `go mod download` against `go.sum`; that runs no user code.
  - The trusted seed GOCACHE as an overlay lower layer at `/work/gocache`.
  - `/work` is a tmpfs, size `limits.work`, rw.
  - `/tmp` → `/work/tmp`.
  - `/dev/{null,zero,full,random,urandom}`.
  - Fresh `/proc` with `hidepid=2`.
  - A generated `/etc/{passwd,group,hosts}`.
- **Environment (complete):**
  `PATH=<go>/bin GOROOT=<go> GOCACHE=/work/gocache GOMODCACHE=/gomod GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS=-mod=mod HOME=/work/home TMPDIR=/work/tmp`.
  Nothing else; no worker variable ever passes.
- **Command:** `go build -o /work/eval ./<bob module>` and then `/work/eval`. Both run inside the
  sandbox.
- **Output:**
  - The evaluator writes graph IR JSON to **fd 3**. It writes diagnostics to stderr, which is
    capped and shown with `-v`.
  - Exit 0 means success. The parent caps fd 3, kills on overflow, then validates the IR.
- **Order of setup:**
  1. Parent: create the cgroup leaf, then
     `clone3(CLONE_INTO_CGROUP | NEWUSER | NEWNS | NEWNET | NEWPID | NEWIPC | NEWUTS | NEWCGROUP)`.
     Map 0 → worker uid; `setgroups=deny`; `Pdeathsig=KILL`.
  2. stage1: make `/` rprivate; tmpfs root; binds, then remount each ro,nosuid,nodev; proc; etc;
     `pivot_root` and detach the old root; remount `/` ro; set the hostname; write
     `max_user_namespaces=1`; `PR_SET_DUMPABLE=0`. Then start stage2 and stay as pid 1 (reaper).
  3. stage2: new userns mapping 1000 → 0; `LockOSThread`; `NO_NEW_PRIVS`.
  4. stage2: Landlock with the best available ABI:
     - fs: `/nix/store` rx, `/src`, `/gomod`, `/etc` and `/proc` r, `/work` rwx, the dev nodes rw;
     - net: TCP bind and connect handled with no rules;
     - scopes: abstract unix and signal.
  5. stage2: seccomp with TSYNC; then `execve`.
- **Seccomp:**
  - Returns EPERM for: ptrace, process_vm_*, pidfd_getfd, mount-family incl. new mount API,
    pivot_root, chroot, unshare, setns, keyctl/add_key/request_key, bpf, perf_event_open,
    userfaultfd, io_uring_*, kexec_*, *_module, reboot, swap*, acct, quotactl, syslog, clock and
    time setters, open_by_handle_at, name_to_handle_at, vhangup.
  - `clone3` returns ENOSYS, so callers fall back to clone. `clone` with any `CLONE_NEW*` flag
    returns EPERM.
  - `socket` is allowed only for AF_UNIX, AF_INET and AF_INET6; every other family returns
    EAFNOSUPPORT.
  - A wrong arch kills the process.
- **Refusal:** a worker without a delegated cgroup or Landlock refuses to start, with
  `✗ worker cannot isolate evaluations: <reason>`.
  `hint: run the worker under systemd with Delegate=yes (e.g. systemd-run --user -p Delegate=yes …, or a unit with Delegate=yes)`.
- **Error texts (from the spike, exact):**
  ```
  ✗ evaluating bob.go took longer than {timeout} and was stopped
    hint: bob.go must only describe the build; long work belongs in a build step (bob.Run)
  ✗ evaluating bob.go used more than {mem} of memory and was stopped (peak {peak})
    hint: a build definition should need far less; look for unbounded loops or huge in-memory data
  ✗ evaluating bob.go produced more than {outmax} of graph output and was stopped
  ✗ evaluating bob.go failed after trying to start more than {pids} processes or threads (exit {n})
    hint: bob.go must not start processes
  ✗ evaluating bob.go failed (exit {n})        (+ compiler/runtime stderr, mapped to repo paths)
  ✗ internal error: the evaluation sandbox could not be set up (exit {n}); this is a bob bug
  ```
  Classification order: timeout > `memory.events oom_kill` > output cap >
  `pids.events max` + non-zero exit > setup exit codes 120/121 > other.

### 5.3 Limits defaults [proposal]

| Limit | Default | Basis |
|---|---|---|
| Evaluation wall clock | 120 s | cold evaluation 13.5 s [8c/15G], 18.8 s [2c/4G]; seeded 0.6 s |
| `memory.max` (includes `/work` tmpfs) | 2 GiB | cold peak 330 MB |
| `memory.swap.max` | 0 | |
| `pids.max` | 512 | peak 69 (cold, 8 CPUs); `go build -p` grows with CPUs |
| `cpu.max` | 2 CPUs | low-power target (D47) |
| `/work` tmpfs | 1 GiB | counts toward memory.max |
| graph output (fd 3) | 64 MiB | |
| stderr kept | 1 MiB | |
| Build jobs (Nix inside the job cgroup) | job leaf with memory/pids/cpu from worker labels; Nix `use-cgroups` optional | verified it runs rootless; per-build limits not tested |

### 5.4 Build step hardening on workers [proposal]

1. IR → derivations is **trusted worker code** (backend B). The evaluator never hands `.drv`
   files to Nix.
2. **Fetches:**
   - Every IR `fetch` node is downloaded by bob's Go fetcher on the worker.
   - Only `https` and `http` to public addresses are allowed. The resolved IP is checked in
     `net.Dialer.Control`, which rejects loopback, RFC1918, CGNAT, link-local (incl.
     169.254.169.254), ULA and multicast.
   - Downloads have a size cap and are verified against the `bob.lock` hash.
   - The result is imported with `nix store add --mode flat|nar --hash-algo sha256 --name <name>`.
     The path is identical to the FOD's (verified), so Nix never builds a user FOD.
   - The fetcher itself is unverified (not written in the spike).
3. **nixpkgs FODs** that miss the cache come from the pinned nixpkgs, which is trusted code. They
   are allowed but still get host network. Workers must not run unauthenticated loopback services.
4. **Store integrity:**
   - Rootless worker: each job runs Nix with an overlay `/nix` (lower = persistent store, upper =
     job tmp). After the job, only the job's new outputs are promoted to the persistent store and
     the cache. Paths that already exist are never promoted, so a poisoned input copy dies with
     the upper layer.
   - Root worker: bob's Nix uses `auto-allocate-uids = true` (and `use-cgroups = true`), so the
     store is root-owned and builds run as allocated uids.
5. Developer machines keep ADR 0002 as it is (one user, own store). A local build can still
   corrupt the user's own store. That is accepted, but `bob doctor` should offer
   `nix store verify --check-contents` (proposal).

### 5.5 Test-service API for `bob.go` [proposal]

```go
// package bob (framework core)
type Service interface{ serviceSpec() *ServiceSpec } // sealed: built only by constructors

type ServiceSpec struct {
    Name    string            // "postgres"; used in errors and logs
    Start   Cmd               // tool + args; runs inside the test sandbox
    Ready   Probe             // TCPReady(port) | CmdReady(cmd)
    Env     map[string]string // exported to the test, e.g. PGHOST, PGPORT, DATABASE_URL
    Timeout time.Duration     // readiness timeout, default 30s
}

// package bob/services/postgres
func Service(p *bob.Project, o Options) bob.Service
type Options struct {
    Version   string   // nixpkgs major, e.g. "17"; pinned through bob.lock
    Databases []string // created after start
    Init      bob.Source // optional SQL files run after start
}

// generic: a service built from this repo
func bob.ServiceFrom(bin bob.Binary, o bob.ServiceOptions) bob.Service

// use (Go ecosystem; same field on every ecosystem's Test constructor)
api := gobuild.Test(p, "integration", gobuild.TestOptions{
    Packages: []string{"./api/..."},
    Services: []bob.Service{
        postgres.Service(p, postgres.Options{Version: "17", Databases: []string{"app"}}),
        bob.ServiceFrom(auth.Server(p), bob.ServiceOptions{Args: []string{"--listen", "$ADDR"}, Ready: bob.TCPReady(8080)}),
    },
})
```

Semantics:
- Each service instance gets its own loopback address: `127.0.0.2`, `.3`, and so on. They keep
  their default ports, so there is no port juggling (both verified). The address is exported as
  `<NAME>_ADDR`, plus the service-specific variables.
- Startup order is the list order. The test starts after every `Ready` passes.
- Teardown: SIGTERM, then 5 s, then the pid namespace ends. Leftovers are impossible (verified).
- A service failure gives
  `✗ //api:integration: service postgres did not become ready within 30s` + the last 20 log lines.
- The test result is cached like any other test (R-FW-024).
- Optimisation [unverified]: make `initdb` a cached derivation and copy it in. That saves about
  0.8–1.3 s per Postgres test.

## 6. Decisions, risks, open points

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S13-1 | Evaluator isolation is rootless and built into `bob` (no bubblewrap, no runc, no daemon). | Principle 6 (only `bob`) and D11; all layers are reachable from Go without cgo. |
| S13-2 | Seccomp and Landlock are written by hand on `x/sys/unix`. No libseccomp; go-landlock is not used. | Principle 8. About 200 lines; libseccomp needs cgo. |
| S13-3 | Workers refuse to run without a delegated cgroup and Landlock (ABI ≥ 4). | Principle 5: limits must be structural. |
| S13-4 | Local `bob build` uses the same evaluator sandbox, without cgroup limits when no delegation is available. | Same results locally and on workers (R-FW-008); costs about 20 ms. |
| S13-5 | Workers don't run user-influenced FODs; bob fetches and imports. | Principle 5; FOD = host network (measured). |
| S13-6 | The Go build cache uses a trusted seed as a read-only lower layer plus a per-evaluation upper layer. | Prevents cross-evaluation cache poisoning; 13.5 s → 0.56 s. |
| S13-7 | Services in tests get one loopback IP each, with their default ports. | Verified 127.0.0.2 and parallel 5432; simplest for users. |

### Owner decision needed

**OD-S13-1: store integrity on workers.** A rootless build can rewrite input store paths
(measured). Options:
- **a)** Rootless workers only, with a per-job overlay `/nix` (tested; about 9 ms + db copy-up) and
  promotion of new outputs after the job.
- **b)** Workers must run as root and use Nix `auto-allocate-uids` (tested; no passwd setup). This
  adds a privileged start, which is against "fully rootless".
- **c)** Both: rootless by default with **a**, and **b** automatically when the worker is started
  as root.

Recommendation: **c**. Rootless keeps zero-setup workers. Root gives a second kernel uid, so an
escape from a build does not reach the worker's credentials.

### Open risks

1. **Overlay `/nix`:** changing the lower layer while overlays are mounted (promotion or
   substitution by a parallel job) is documented as undefined behaviour in overlayfs. The design
   may need a promotion lock or a two-store setup. Untested.
2. Db copy-up cost on large stores (100k+ paths) is not measured. Nix's experimental
   `local-overlay` store is an alternative; not tested.
3. Nested user namespaces stay **allowed inside Nix builds** (`unshare -U` OK). Nix's seccomp
   does not block them. This is kernel attack surface for every build. bob can't filter it without
   patching Nix, unless builds get bob's own seccomp layer (possible with `sandbox-paths`? unknown).
4. Rootless: evaluator, builds and worker share one kernel uid. A kernel escape gets the worker's
   fleet credentials. R-SEC-001 limits the damage; root mode (OD-S13-1 c) reduces it.
5. amd64: the seccomp filter compiles for amd64 but was never run. It also needs an x32 check
   (`nr & 0x40000000`).
6. Landlock is not available on older kernels (< 5.13) or when it is disabled in `lsm=`. Workers
   refuse then (S13-3); this may exclude some distros. Not surveyed.
7. Ubuntu 24.04 AppArmor (ADR 0002 risk 1) also applies to evalbox's nested namespaces. Untested.
8. Third-party Go modules imported by `bob.go` (cross-repo, R-FW-022) are not in the seed cache.
   They compile on every evaluation unless there is a per-project cache layer, which needs its own
   trust rule (protected branches write, PRs read).
9. The egress-guarded fetcher (§5.4 point 2) and DNS rebinding handling are not implemented.

### Still to verify

- A quiet-machine rerun of `measure.sh`.
- The full worker flow: evaluate → IR → drv → overlay build → promote.
- `use-cgroups` per-build limits.
- The services from §4 marked unverified.

### Host changes

- Files under `/home/bobtest/s13*`, `/home/bobtest/.config/bob-worker/credentials` (fake secret)
  and `/home/bobtest/.local/share/bob-s13` (store with a deliberately poisoned test path).
- `/root/scratch-s13/rootstore` (scratch store).
- Tiny test paths in root's `/nix/store`: `s13-*` and `s13-fodeq.txt`.
- No `/etc` changes, no linger, no background processes left.
