# I-X1 progress (host safety: /dev/null clobbered twice, ~18:36 and 21:06 on 2026-10-22)

## cause (reproduced)
- `tests/cli/completion_test.go` TestRCLI004Scripts sourced the zsh completion script with
  `zsh -f -c "autoload -Uz compinit && compinit -u -d /dev/null; source <script>"`.
- `compinit -d FILE` sets the dump file; zsh's `compdump` (zsh 5.9.1,
  share/zsh/5.9.1/functions/compdump lines 21-36, 138) writes `FILE.$HOST.$$` next to it and then
  runs `mv -f FILE.$HOST.$$ FILE`. As root, /dev is writable, so `/dev/null` was replaced by a
  regular root-owned file (the dump: `#files: 996 version: 5.9.1`, `_comps=(` …, ~52 KB).
- Every run of tests/cli as root did this (also the manual zsh check of I-M6a at ~18:36; the
  21:06 incident matches a tests/cli run).
- Reproduction without touching the host: `unshare --mount --propagation private`, tmpfs on
  /dev, `mknod /dev/null c 1 3`, the old zsh command → `/dev/null` is `regular file root 52159`;
  host /dev/null stayed a character device (checked).

## other candidates checked (no host /dev/null path found)
- pkg/engine/store/ns.go (bind + pivot mode), pkg/engine/eval/evalbox (stage1 tmpfs /dev with
  bound nodes, new mount ns, MS_PRIVATE), internal/isolation (job container, CLONE_NEWNS +
  MS_PRIVATE, worker RemoveAll of the job dir only sees host-side empty dirs), tests/rootless
  enter (/dev rbind inside an unprivileged user+mount ns), pkg/builder (writes confined to
  outputs/build dir; fuzz token "/dev/null" is refused lexically; no remove/rename ops),
  harness/serverenv/devstack cleanup, all Remove/RemoveAll/Rename call sites in pkg, internal,
  tools, tests; nix.conf (sandbox = true everywhere; clients run as bobtest).
- Note for later (not a host risk today, not changed): in pivot mode the rbind of a top-level
  dir that contains <home>/nsroot also carries the binds made earlier (e.g. nsroot/dev), so a
  RemoveAll(<home>) run *inside* bob's namespace would descend into host /dev. Nothing does that
  now; worth a guard if that cleanup is ever added.
- A 1 s watcher (stat -c %F /dev/null with ps snapshots, /var/tmp/ix1-devnull.log) ran during
  the investigation; no change was seen; it is stopped.

## fix
- tests/cli/devnull_test.go (new): `zshSourceArgs` = `compinit -u -D` (no dump file);
  completion_test.go uses it (one line; protected file, assertion unchanged).
- Regression: TestZshCompletionCheckKeepsDevNull runs, in a private mount namespace on a tmpfs
  /dev (the script refuses to start zsh unless /dev is that empty tmpfs), the old command
  (control: must become a regular file) and `zshSourceArgs` (must stay a char device), then
  checks the host /dev/null. PASS (0.5 s).
- Guard: tests/harness/devnull.go (new) `CheckDevNull` (char device 1,3) and `Main(m)`: fails
  the test binary with exit 3 and a HOST DAMAGE message naming the binary when /dev/null is
  wrong before or after its tests. TestMain added (new files) in tests/harness, tests/cli,
  tests/devenv, tests/install. TestCheckNull covers it with temp files only.

## verification
- go vet ./tests/harness ./tests/cli ./tests/devenv ./tests/install: clean; gofmt clean.
- go test ./tests/harness -run TestCheckNull: PASS. go test ./tests/cli -run
  'TestZshCompletionCheckKeepsDevNull|TestRCLI004Scripts': regression PASS; TestRCLI004Scripts
  fails only for pre-existing reasons (completion goldens not yet approved, fish not on this
  shell's PATH); its zsh sourcing step reports no error with -D. Host /dev/null intact after.

## follow-ups
- Protected manifest: completion_test.go changed (reported by CI; re-hash with owner OK).
- Agents: never point a tool's write target (dump/history/lock/-o for tools that rename) at
  /dev/null when running as root.
