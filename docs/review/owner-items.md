# Owner items

## FIX-B

Test-side fixes for the OPEN findings of docs/review/gate-h-B.md (scope tests/cli, install, devenv, outputs, platforms). Items below are decisions or SPEC gaps that remain.

1. **R-CLI-002 schema.** `schema/events-v1.json` is named "protected" in SPEC §02.4/R-CLI-002 but does not exist in the repository. The test keeps its table validator (`validateEvent`, derived from the §02.4 field table) and `checkSchemaFile` still returns silently when the file is absent. Decision needed: the owner supplies the schema (then `checkSchemaFile` must validate against it and fail when it is missing), or the SPEC text is changed.
2. **R-OUT-009 layer order.** SPEC defines popularity and ordering but the acceptance only states 14 layers, one differing layer blob, <= 16 % new bytes, and `MaxLayers: 4` with the root alone in layer 3. The "glibc alone in layer 0" assertion was removed (a correct implementation can put glibc's own dependencies first). Asserted now: root alone in layer n-2, customisation layer last, one path per layer in front of the shared layer. Not asserted: the popularity order itself (needs the closure graph from `nix path-info -r`); decide whether a closure-based check is wanted.
3. **R-OUT-006 "network disabled".** The SPEC wording cannot be tested by rebuilding offline (the warm build already realised everything). The test warms online, then requires the offline (`unshare --net`) rebuild to succeed as a no-op, and keeps the "no fixed-output bundle derivation" check as the evidence that the builders need no network.
4. **R-CLI-008 excerpt.** Read as: the excerpt (its last 10 lines at most) equals the last lines of the log, in order, with `…/` rewritten store paths as wildcards. If the SPEC intends a looser "match", say so.
5. **R-CLI-010 proxy.** The `--update golang.org/x/net` scenario uses a `file://` GOPROXY passed as `BOB_GOPROXY` (the variable already used by tests/eco/golang). If the config key for the Go proxy differs, the scenario must be adapted. SPEC §03.2.4 only says "configurable in bob's user config".
6. **R-INST-001 kill points, R-INST-005 GC roots, R-INST-002 beyond precondition, devenv LOW items.** Not changed in FIX-B (not requested): they need either a SPEC-defined kill-point hook (SPEC problem) or new assertions; see gate-h-B.md.
# Owner items

## FIX-C
Ambiguous or unsatisfiable items left as they are (tests stay as written; nothing loosened).

- R-NPM-007a: SPEC asks for "ESLint (type-checked) as tests", but `pkg/npm` has no ESLint API (`npm.Test` runs only Vitest or `node --test`, §03.4.1). SPEC problem: an ESLint entry point (a Runner or a check package) must be specified before a test can assert it. No test added.
- R-NPM-004a: SPEC says "the test fails ... the test opens `:memory:`", but the native fixture has no test target (the test uses `bob run`). Also "builds offline" is not observable black-box (needs a netns around the build, the store's builder is not ours). Decide: add a `//native:test` target to the fixture and a netns-wrapped build.
- R-NPM-010a: SPEC budget "within the budget (§03.8)" is B-082 (50 ms server side), owned by the budgets stage; the test asserts 2 s end to end. The fixture is Svelte (App.svelte); SPEC says App.tsx.
- R-RS-005: `bob build --dry-run` is not in the §02 CLI table (SPEC problem). "Empty crate cache": the cold run uses a fresh BOB_HOME (drops every cache, may be stricter than SPEC) in a user+net namespace; owner to confirm that is intended.
- R-RS-002a: the exact Cargo.toml of the four S3 graphs (12/25/173/393 packages) are not in the repo; the test spells them from docs/spikes/s03-rust.md 3.2 (serde_json, tokio full, web, big) without a package-count assertion.
- R-GO-002b: the "bob itself" corpus entry is a scratch module requiring bob's direct requirements, not bob as a workspace. Network isolation of `go build`/`go vet` rests on GOPROXY from `bob env`.
- R-GEN-005: the "added cost <= 0.5 s" baseline is S6's 0.93 s (fixture without two-phase planning); the test compares the median of five no-change builds to 0.93 s + 0.5 s = 1.43 s (SPEC's own number). A baseline measured on the test host (a build of the same fixture without codegen) cannot be produced by the test; decide whether to add a budget row.
- R-RS-001a: output content of demo/usec/cmain is not asserted (only exit 0); SPEC states no expected text.
