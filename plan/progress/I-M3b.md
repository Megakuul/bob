# I-M3b progress (M3.5 evalbox sandbox)

## Done
- M3.5 evaluator sandbox, `pkg/engine/eval/evalbox` (new) + wiring in `pkg/engine/eval`.
  - evalbox: parent / stage1 (pid 1) / stage2 re-executions of /proc/self/exe, dispatched by an
    `init` hook; user+mount+net+pid+ipc+uts+cgroup namespaces; tmpfs root with read-only binds;
    /work tmpfs (size cap); seed GOCACHE as overlay lower; /dev nodes; /proc hidepid=2; generated
    /etc; pivot_root; max_user_namespaces=1; nested userns uid 1000; no_new_privs; Landlock (best
    ABI, TCP deny, abstract-unix + signal scopes); seccomp deny-list (TSYNC, x32 kill, clone NS
    flags, socket families); fd-3 graph cap; stderr tail cap; wall clock; delegated cgroup leaf
    (memory.max, swap 0, oom.group, pids.max, cpu.max, cgroup.kill) when bob is alone in its
    cgroup, else RLIMIT_NPROC + polling memory watchdog (G13). Strict (worker) mode refuses
    without cgroup or Landlock ABI >= 4. S13 §5.2 error texts and classification order.
  - eval: toolchain discovery (go on PATH), framework discovery (runtime.Caller → module root),
    minimal evaluator generation (.bob/eval/{go.mod,go.sum,overlay.json,main.go}, virtual main
    _bobeval), trusted seed GOCACHE per toolchain in $XDG_CACHE_HOME/bob/eval-seed, Evaluate.
- Tests green: `go test ./tests/sandbox` (root, this host: all 15 + LayerErrnos; root in a
  delegated scope: all 15 via cgroup; bobtest without delegation: 12 + 3 skips; bobtest in
  `systemd-run -p User=bobtest -p Delegate=yes`: all 15). `-tags bob_nolandlock`: green.
  `go test ./pkg/engine/eval/evalbox` green. CI `-only fmt,vet,lint,layering` green.

## DECISIONS
- D-M3b-1: Sandbox code lives in `pkg/engine/eval/evalbox` (E3, std only), callable by the
  worker directly (`evalbox.Run`, `Spec.Strict`). eval.DisabledLayer = evalbox.DisabledLayer
  (the three layer tag files moved there).
- D-M3b-2: Stages re-exec /proc/self/exe and are dispatched from a package `init` (the only way
  that also works inside test binaries); they end with `syscall.Exit` — they are separate
  programs, never bob's main, so the AGENTS §5.3 cleanup rationale does not apply.
- D-M3b-3: When bob runs as root, the sandbox never runs as host root: the outer userns maps
  0 → 65534 (nobody) and the child setuid(0) into it. Escapes land as nobody and RLIMIT_NPROC
  applies (it is bypassed for kuid 0). Because such a uid can neither reach paths under /root nor
  bind a descriptor of another mount namespace, root clones each bind source with
  open_tree(OPEN_TREE_CLONE) + mount_setattr(RDONLY|NOSUID|NODEV) and stage1 attaches it with
  move_mount (measured on this host). Unprivileged bob passes paths.
- D-M3b-4: Delegated cgroup is used only when bob is alone in its cgroup (it then moves itself
  into `<own>/bob-supervisor` and enables memory/pids/cpu on `<own>`), never rearranging a shared
  cgroup (a login scope). Otherwise: RLIMIT_NPROC = Pids, and a 10 ms watchdog summing
  RssAnon+RssShmem of the sandbox tree + /work tmpfs usage enforces MemoryBytes (G13).
  Pids classification without cgroup: non-zero exit and (EAGAIN text from fork/thread creation
  in stderr, or peak tasks >= Pids).
- D-M3b-5: Seed GOCACHE (S13-6) keyed by Go version + GOROOT + GOARCH, built once (std +
  framework pkg/bob/...) inside the sandbox with the seed bound read-write, then renamed into
  place; evaluations mount it as overlay lower. Cold seed ~14-15 s [8c/15G], warm evaluation
  ~0.4-0.6 s per hostile case.
- D-M3b-6: Until R-FW-043/M3.4: the toolchain is `go` on PATH, the framework is the source tree
  this bob was built from, bound at /fw and wired with `replace github.com/megakuul/bob => /fw`;
  the go directive is raised to the framework's (go would otherwise rewrite the read-only
  go.mod). The evaluator gets the pkg/bob Main request as one JSON argument
  (`{"targets":[patterns]}`). No IR on fd 3 → error "evaluating bob.go failed" + raw diagnostics
  (M3.4 maps the `bob::` lines).
- D-M3b-7: Error type `evalbox.Error{Msg, Hint, Detail, Exit}` without "✗ " (E3 cannot import
  diag; the CLI renders it). Limits render as S13: "5s"/"120s", "512M"/"2G", "16 MiB".

## NEEDS (other units)
- M3.4 (R-FW-043): eval key / reuse of .bob/eval/bob-eval, package main renames (acme
  cmd/api fails today: "is a program, not an importable package"), dependency bob repos, compiler
  message path mapping, `bob::` diagnostic mapping, IR validation (ir.Parse) and pinned Go;
  embedded framework module zip replaces the /fw source bind (findFramework fails in -trimpath
  builds).
- M13.7: worker calls `evalbox.Run` with `Strict: true` (refusal text S13-3) and worker limits.

## TEST PROBLEM
- tests/sandbox under `-tags bob_noseccomp`: case `sockets`, attack "AF_VSOCK socket" reports
  LEAK. Without seccomp no remaining layer can refuse socket(AF_VSOCK): Landlock has no socket
  family control (7.2 UAPI headers: fs, net TCP/UDP, scopes only), and vsock is loaded on this
  VM (virtio transport). Measured: socket(40, SOCK_STREAM) succeeds in a fresh userns+netns.
  S13 only measured escape/network/secrets with seccomp removed, not sockets. The test's
  "same expectations hold" cannot hold for this attack on a host with vsock. Possible extra
  layer (not done): stage1 sets net.vsock.child_ns_mode=local and gives stage2 its own netns, so
  connect fails (measured ENETUNREACH) — but socket() still succeeds, so the test would still
  report LEAK. Owner decision needed on the test expectation.

## Regression check
- tests/core + tests/sandbox before (Evaluate stub) vs after: no PASS→FAIL; +3 PASS
  (TestRFW011Memoised, TestRFW045HostileCases, TestRFW045LayerErrnos). The tests/core tests that
  were red now fail on M3.4 / L2 work (package main bob.go, missing rust/cc/proto APIs).
- Incident: during that comparison eval.go was temporarily stubbed in the shared tree for about
  10 minutes (14:14-14:25, an aliased `cp -i` blocked the restore). Restored and re-verified; a
  tests/core run by another unit (I-M3c) in that window may have seen "not implemented" reds.
