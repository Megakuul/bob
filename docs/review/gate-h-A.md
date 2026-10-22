# Gate H test review, unit GH-A (R-ARCH-*, R-FW-*, R-NIX-*)

Scope: tests/core, tests/sandbox, tests/nix, tests/rootless/*_test.go. Reviewer did not write them.
Method: SPEC acceptance text per id vs the asserting code, red path (harness.Red/RedFeature), vacuity,
tolerances. `go vet ./tests/core/... ./tests/nix/... ./tests/sandbox/... ./tests/rootless/...` passes after the
fixes. Nothing was run against an implementation (all features are stubs), so wrong-implementation scenarios are
derived by reading. Checkpoint: plan/progress/GH-A.md.

## A. Fixes applied (test-side bugs, minimal edits)

| id | file:line | bug | fix |
|---|---|---|---|
| R-NIX-003 | tests/nix/layer_test.go:~325 | expected `numhex=2a`; SPEC (R-FW-013), every other test and the fixture say `numhex=3432 proto_bytes=116`. A correct implementation fails. | assert the full SPEC line `digest=<sha256 bob> numhex=3432 proto_bytes=116` |
| R-FW-022, R-FW-013(cross-repo) | tests/core/testdata/repos/cross-repo/{app/use,dep/libs/bar}/bob.go | steps ran `Cmd: true` with no Tools (PATH holds declared tools only, R-FW-023) and declared outputs (`out.txt`, `lib.txt`) they never create (build error per R-FW-023). `bob build //use:consumer` must exit 0, so a correct implementation fails. | steps declare nixpkgs bash+coreutils and write the outputs |
| R-FW-020 | tests/core/steps_test.go:~425-470 (chainRepo) | (1) steps ran bare `true` without Tools (same as above); (2) release and debug subtests built identical derivations, so the second was a cache hit, not "cold at both profiles"; (3) s000 was built before the timer, so 99 steps were timed. | steps declare coreutils; per-variant salt in each step env; warm-up is a separate `//chain:warm` step, all 100 steps timed |
| R-FW-060 | tests/core/native_test.go:~160 | `readFile(.gitignore)` is fatal if absent; none of the fixtures has a `.gitignore` and bob never creates one, so a correct implementation fails after generating everything. | `readOrEmpty` |
| R-FW-061 | tests/core/native_test.go:~232 | "files unchanged" hashed the whole tree including `.bob/` (evaluator cache etc. legitimately change), a correct implementation can fail; the SPEC template (golden file absent) was not asserted at all. | hash skips `.bob/`; for the Go case the template lines of SPEC (first line, `+ golang.org/x/text v0.20.0  (not declared in bob.go)`, both hint lines) are asserted |
| R-FW-025 | tests/core/steps_test.go:~360 | `ms, _ := e["durationMs"]`: a missing field gave 0 and passed the "≤ 3 s" bound. | missing or non-positive durationMs is an error |
| R-NIX-002 | tests/nix/layer_test.go:~345 | (1) a failed build with no `error` event (crash) passed; (2) the two `strings.Replace` edits silently did nothing if the fixture text changed. | exit != 0 requires an error event; precondition check on the edits |
| R-FW-016 | tests/core/label_test.go:~190 | only a heuristic ("line contains `(p`, not `bob.Define(`"); no golden exists, so a wrong line (e.g. a nested call line) passed. | exact constructor line pinned for //libs/foo:foo, //crates/rcore:rcore, //web:web |
| R-FW-005, R-FW-062 | tests/core/lock_test.go:~277,~345 | `strings.Contains(e.Message, "go")` matches "bob.go"/"golang" in any message with a position, so a stale-lock error naming the wrong ecosystem passed. | `namesEcosystem` word match (not preceded by `.`/word char, not followed by word char) |
| R-FW-065 | tests/core/lock_test.go:~21 | "for every fixture" but only 7 of 11 fixtures were locked twice. | all 11 fixtures |
| R-ARCH-004 | tests/core/layout_test.go:~124 | the `bob --help` scan passes if the help lists no commands at all. | at least one command line must be parsed |
| R-FW-063 | tests/core/pipeline_test.go:~125 | the "no generated FOD" loop passes if no bob-builder drv is found (wrong store layout). | require at least one inspected drv |
| R-NIX-024 | tests/nix/embedded_test.go:~170 | asserted mode 0644 for `user.conf`; SPEC gives no mode (only "empty"). Stricter than SPEC. | mode dropped, existence and emptiness kept |
| R-NIX-026 | tests/nix/embedded_test.go:~258 | the "4 concurrent builds" ran after a warm-up build, hiding races in state creation; SPEC states no warm-up. | start on an empty state; checkout made first (the fixture map is not goroutine-safe); Red check on result 0 |

## B. Findings not fixed (semantic or SPEC-ambiguous), ranked

### High (weaker than SPEC acceptance; Gate H would freeze the gap)

1. **R-FW-031** `platform_test.go:203` TestRFW031VariantMatrix. SPEC: the 11 configurations of S7 §3.3 and the golden count 99. The test builds 8 configurations (no feature `shout`, no `cc.CrossLibs`, no amd64 asan+ubsan rows) over two roots, and the golden `tests/goldens/outputs/cross-drvcount.txt` does not exist. Blessing it from the implementation would freeze whatever number comes out. Wrong implementation that passes: any projection that happens to give a different count once blessed, and missing feature/Cross-libs sharing. Needs: the 11 configs and the golden seeded with 99 before Gate H.
2. **R-FW-038** `platform_test.go:684,697`. SPEC: "for each row" of the 13-row table. Tested rows: C/C++ library, Rust target crate, Rust host unit, Rust build-script run (4 of 13; no Go pure/cgo/link (race ✔, asan only pure), `nixpkgs.Lib`/`Tool`, fetch, codegen, npm, OCaml, OCI). A Go projection that ignores `+race` passes. Also the unit slugs `rdemo-build-run`, `rdemo-build-script`, `rmac`, `crates-rcore-rcore` are guesses: §01 R-FW-038 names `<dir-label>-<name>` while §03.3.3 names Rust units `<name>-<version>[-host]-build-run`; with the §03.3.3 names `drvNamesWith` returns nothing and R-FW-035/038 fail a correct implementation. Needs a SPEC decision on the Rust derivation names.
3. **R-FW-045** `tests/fixtures/evil/evil.go:73-82` (used by tests/sandbox). SPEC: "the three cgroup-only cases" may be skipped without delegation. Four cases are `NeedsCG` (forkbomb-chain, forkbomb-tree, threadbomb, membomb). membomb is skipped although the SPEC local defaults say memory is limited "only with delegation", so SPEC is itself ambiguous (S13 table: "3 bomb cases" are the pids ones). One more skipped case than SPEC allows; decide, then fix the fixture or the SPEC wording.
4. **R-ARCH-005** `layout_test.go:146`. SPEC: ends with "a `bob` built by bob" whose embedded builder path equals builders.go. The test never checks the final bob was built by bob (a script that prints the `go build -tags bootstrap` binary passes) and proves path equality only indirectly (`bob doctor` exit 0 and no BOB-E-9007; nothing requires doctor to do the check). Wrong implementation that passes: bootstrap.sh that stops after the first bob.
5. **R-FW-046** `ir_test.go:46`. Acceptance: "equal config projections **and equal derivation paths**". The code comment says "checked through the build" but only the IR is checked. Wrong implementation: projection equal in IR, drv path differs per profile.
6. **R-FW-063** `pipeline_test.go:94`. Path identity is asserted only for recursive `narHash`/`tree` values. SPEC also requires it for flat hashes (`FixedOutputPath` with hash mode flat, crate `checksum`, npm `integrity`, `sources` `hash`). Which lock keys are imported flat is not specified, so not fixed. `:204` (no Nix build activity for sources) is vacuous if `-vv` does not print `@nix ... "building '…drv'"` lines; no guard that at least one build line was seen.
7. **IR/native/drift/conflict/event goldens do not exist** (`tests/goldens/ir/{polyglot,acme}.json`, `native/**`, `native/drift/*`, `native/codegen/conflict.txt`, `events/polyglot-cold.jsonl`, `outputs/*`). golden.Assert writes a candidate when none exists, so at Gate H the first implementation output becomes the protected expectation. For R-FW-061 (template in SPEC), R-FW-066 (S6 conflict text), R-FW-016 (definedAt lines) the SPEC text should be pinned before that. (R-FW-061 Go case is now asserted by line, see A.)
8. **R-NIX-012** `faults_test.go:122`. SPEC: error blocks of faults 6–15 and 22–33 have ≥1 excerpt line, the hint and `full log: bob log <label>`. The wrapper only checks `error[BOB-E-` and `package defined at`. Recorded goldens contradict the SPEC: faults 26, 28, 30, 31, 33 have no excerpt line (31 also no `full log`), 06–09 and 15 have no `hint:`. Tightening would fail on the protected goldens, so SPEC vs golden must be reconciled first.
9. **Budget clauses inside acceptance text without any test**: R-NIX-009 (10,000 source dirs ≤ 5 s, B-076), R-NIX-027 (cold ≤ 2 s / warm ≤ 50 ms, B-078), R-NIX-020 (first extraction ≤ 500 ms), R-NIX-021 (namespace entry ≤ 10 ms), R-NIX-030 (B-010..013). All `bench` fields in tests/budgets/budgets.json for these ids are empty and the tests only reference the ids. Per the brief ("unless the acceptance text is the budget") a test should exist; confirm the budgets stage owns them before Gate H.
10. **R-NIX-021** `namespace_test.go:65`. Acceptance: probes F1/F3 "in bind and pivot mode as non-root". The bind-mode probe runs as whatever user runs the tests (root on this host; the test logs a note); pivot mode is covered as non-root in tests/rootless. Bind mode non-root only appears indirectly (R-FW-001 as bobtest). Wrong implementation that passes: namespace code that works only for uid 0 with a host /nix.

### Medium

11. **R-FW-034** `platform_test.go:409`: rust-std contents are compared with the manifest copy inside the derivation itself, not with `lib/rustlib/manifest-rust-std-<triple>` of the target `rustc.unwrapped`. A derivation holding half the files plus a truncated manifest passes.
12. **R-FW-036(b)** `platform_test.go:587`: human mode reads stdout only (the note is on stderr), so bob is not stopped at the note and runs until the 4-minute kill; "before any build" is asserted only in `--json` mode. A human-mode implementation printing the note after builds start passes.
13. **R-FW-066(f)** `native_test.go:457`: kill points are a wall-clock sweep (40 points over the first cold run's duration); later runs are faster (warm), so many kills land after bob finished, and the "temp file" check is by tree equality only. SPEC says fault injection at each step; no hook is defined. Not guaranteed to hit the write protocol.
14. **R-FW-067(c)** `native_test.go:567`: runs `bob dev --servers-only --no-servers` (contradictory flags; an implementation may reject the pair with exit 2, then the test fails for the wrong reason) and a fixed 3 s startup sleep; the 2.5 s measure includes startup if the watcher starts later.
15. **R-FW-024** `steps_test.go:229`: SPEC "second `bob test //...`"; the test runs `//cmd/api/...` and `//internal/greet/...` only (narrower; the ≤ no-op budget is not asserted).
16. **R-FW-033** `platform_test.go:327`: cross checks cover only Go (GOARCH/GOOS/CC/CXX/-extld) and `<triple>-g++`; no `<triple>-gcc`/`-ar`, no Rust `--target`/`-C linker`, no native `gcc`, no "every cgo package has CC", no both-hosts × both-targets golden (hardware-limited).
17. **R-FW-030** `platform_test.go:144`: "every drv has `system = aarch64-linux`" checked for bob-builder derivations only; the SPEC literal contradicts R-FW-032 (target libs have the target system), so the narrowing is reasonable but should be a logged SPEC problem.
18. **R-FW-016** `label_test.go:150,222`: SPEC says `(defined at libs/foo/bob.go:12)`; in the fixture the constructor call is line 13 (line 12 is the `var Lib = bob.Define(` line, which R-FW-001 uses for gopls definition). Tests compute 13. SPEC literal vs fixture: log as SPEC problem.
19. **R-FW-047** `ir_test.go:228`: the "1,000,001 nodes" document has nodes without required fields, so any rejection passes (an implementation without the node cap but with required-field validation passes). Unavoidable with the 64 MiB cap (comment acknowledges); assert the error text or log a SPEC problem.
20. **R-FW-015** `label_test.go:27`: SPEC lists `//a/b` and `//api:api@linux-amd64+debug+asan` among the table strings without "(invalid)"; the table treats both as invalid for `ParseLabel` (defensible, they are a pattern and a configured label). The 5000-byte `dir` is asserted valid although the grammar has no cap and §5.4 suggests one; an implementation that caps lengths fails. Decide and log.
21. **R-FW-004/048** `lock_test.go:~290`: BOB-E-1019 hint follows R-FW-048 text (`…or run bob lock --rebuild to resolve everything again from bob.go`); the error table (SPEC line 1887) says `resolve the merge conflict, or run bob lock`. SPEC-internal conflict; test follows the requirement. The SRI walker (`checkCanonical`) ignores the `hash` key although SPEC `sources[].hash` and opam `hash` are SRI.
22. **R-FW-062** `lock_test.go:~345`: BOB-E-1012 position (`bob.go:<line>`) is asserted for the go case only; rust/npm cases check the ecosystem name only.
23. **R-NIX-006(c)** `drv_test.go:380`: cross-check over cpp-lib, go, polyglot; SPEC says every `examples/*` (rust, npm, cross-repo missing).
24. **R-NIX-008** `layer_test.go:190`: a bob FOD is recognised by `bob-builder`/`bobSpec` in the drv text; a bob-generated FOD with another builder is not flagged. "Unreachable source URL" is modelled by dead proxy variables, which an HTTP client with `Proxy: nil` ignores.
25. **R-NIX-009** `srcs_test.go:128`: "imports only missing paths" is claimed; the second import is asserted to return the same paths but not that nothing was re-imported.
26. **R-NIX-016** `builder_test.go:333`: fuzz escape detection covers `outside/`, the root and one `/tmp` marker path; writes to other places (`/tmp/x`, `/etc/x` as root) are not scanned.
27. **R-NIX-022** `rootless_test.go:236`: the Debian row (`kernel.unprivileged_userns_clone = 0`) of the fix table has no case (acceptance names only max_user_namespaces/ENOSPC; AppArmor manual).
28. **R-FW-002** `bobgo_test.go:227`: R2's "no Go files: dir name lowercased, chars outside [a-z0-9_] removed, prefixed `x`" derivation is not tested (acceptance says one sub-repo per rule, so only the mismatch case).
29. **R-FW-001** `bobgo_test.go:61,~170`: completion check for fixture field steps is `"label":"X` prefix on one character (`golang.BinaryOpts{\n\t\tX`), and `"label":"Lib` is a prefix match for `foo.`; "no diagnostics" steps pass if gopls never published (the later type-error step requires a real publication, so only the unchanged-file steps of other files are weak).
30. **R-FW-012/014** `api_test.go:~215`: constructors that are methods (e.g. `Module.Binary`) are exempt from the Example and one-per-kind checks; interface result types (Tree, Tool, Linkable, …) are exempt from the one-per-kind check.

### Low / informational

- R-ARCH-001: allows `tests/` packages (decision A24, GOWORK=off); SPEC text lists only pkg/cmd/internal/proto.
- R-FW-043: `.bob/eval/key` is only stat'ed; rules 1–4 of the statement are not in the acceptance and not tested.
- R-FW-066(d): asserts the root is gone, not that empty parents and `.gitignore` are gone.
- R-NIX-019: only `bin/` is tested for strip; `--strip-all` vs `--strip-debug` by file kind is not distinguished.
- R-NIX-014: backoff lower bound is 4 s (SPEC 1 s + 4 s = 5 s); size-cap error code not asserted.
- R-NIX-017: `bob::` diag lines and "never uses /bin/sh" (statement) not asserted.
- R-NIX-025: no system daemon is started; host `nix-store --check-validity` is used.
- R-NIX-027 uses a miniature nixpkgs (fakepkgs); polyglot cold/warm budget see 9.
- Red path: no test was found where `harness.Red`/`RedFeature` returns before an assertion that matters for an implemented feature, apart from the cases above (R-FW-036(b), 067(c)).
- tests/core uses `t.Skip` nowhere; tests/sandbox skips the `NeedsCG` cases (SPEC-sanctioned, see 3).

## C. Ids reviewed with no findings

R-ARCH-002, R-ARCH-003 (wrappers around pkg/layering_test.go; A: ARCH-004 guard), R-FW-008, R-FW-009, R-FW-010, R-FW-011, R-FW-013, R-FW-021, R-FW-023, R-FW-044, R-FW-048, R-FW-050, R-FW-051, R-FW-064, R-FW-065 (after fix), R-NIX-001, R-NIX-005, R-NIX-007, R-NIX-010 (delegated to tests/errors runner, out of scope), R-NIX-011, R-NIX-013, R-NIX-014, R-NIX-015, R-NIX-018, R-NIX-020, R-NIX-025, R-NIX-028, R-NIX-029.

Reviewed with fixes only (see A): R-ARCH-004, R-FW-005, R-FW-016, R-FW-020, R-FW-022, R-FW-025, R-FW-060, R-FW-061, R-FW-062, R-FW-063, R-NIX-002, R-NIX-003, R-NIX-024, R-NIX-026.
