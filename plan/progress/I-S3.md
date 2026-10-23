# I-S3 progress (test/fixture corrections, tests/errors replay, registry snapshot recording)

Owner of: tools/regmirror (record modes), tests/eco/internal/ecotest (manifest lookup),
tests/fixtures/registry/README.md; test corrections 30-33 (owner OK), decision A141.

## done
1. **npm constructor calls** (correction 30): the six remaining `npm.App(p, …)` / `npm.Test(p, …)`
   calls are now `npm.NewApp` / `npm.NewTest` (fixtures npm/native, npm/ts-lib, codegen/web;
   tests/eco/npm repos_test, build_test, graphs_test). Types stay `*npm.App` / `*npm.Test`
   (pkg/npm/api.go, SPEC §03.4). Grep over tests/ and fixtures: every qualified call of cc, rust,
   ocaml, golang, npm, oci resolves to a func and every `*pkg.T` to a type (cc.NewBinary/NewTest,
   rust.Test → *rust.TestSet, ocaml.NewTest); no other clash. All fixture bob.go files type-check
   with `-tags bob` except codegen/go/polyglot proto/graphql/openapi packages (API not
   implemented yet: other units) and acme main_test (unrelated).
2. **tests/errors** (correction 31): buildBins builds with `serverenv.BobtestTagsFlag()`. Quick
   tests pass; all 56 replay entries pass (48 in the full run up to entry 59, which hit the 50 min
   wall clock on live entries, plus the 8 `ok-*` replay entries run separately). Live-only entries
   49-58 fail or time out (`bob build` killed): not replay, not this unit.
3. **tools/regmirror record modes** (record.go, tests record_test.go): `-record-npm` (miss →
   registry.npmjs.org; packuments in the abbreviated form, per-version documents as plain JSON,
   both JSON-validated with dist.tarball rewritten to `@REGISTRY@/`; tarballs streamed into the
   blob cache), `-record-opam` (repository snapshot from the GitHub archive of the commit, cache
   entries from opam.ocaml.org/cache), `-opam-commit` (written to index.json; UpdateIndex keeps
   capture/snapshot dates once set). Concurrent misses of one file are serialised; files are never
   re-fetched. Crates recording reuses the same fetcher.
4. **Snapshot location** (A141): the npm manifests exceed 150 MB, so the snapshot is outside git:
   `~/.cache/bob-test/registry-manifests` (or `$BOB_REGISTRY_MANIFESTS`) and
   `~/.cache/bob-test/registry-blobs` (or `$BOB_REGISTRY_BLOBS`). `ecotest.RegistryManifests`
   finds it (tests/fixtures/registry first when it holds index.json); Registry and OpamCommit use it.
5. **graphs_test writeGraph** (correction 33): importer package names = directory names (R2) and
   no unused npm import in the root bob.go. 8 of 10 graphs failed at eval before.
6. **Recording run** (live registry.npmjs.org, mirror on 127.0.0.1:4873 in record mode):
   TestRNPM001a* (all 10 graphs + peer split + hostile docs: all PASS against the recording;
   native and mono needed a rerun after load-related timeouts), then the remaining npm suite
   (TestRNPM002-010) for tarballs. Merged I-M10b's codegen recording
   (/var/tmp/i-m10b/registry, copied with no-clobber; overlapping files were identical).
   Offline check (harness-started mirror, no record mode, manifests found in the cache dir):
   vite-react and peersplit PASS without new fetches; tests/eco/gen run offline on the merged
   snapshot: 3 PASS (RGEN002a undefined type, RGEN003a OpenAPI, RGEN003a bad ref), the failures
   are not registry-related (Go link steps of svc/cmd/{client,server} exit 1, missing goldens
   tests/goldens/gen/{proto-syntax-error,runtime-mismatch-f4,runtime-mismatch-f5}.txt, no-change
   build 4.7 s vs 1.43 s budget on the loaded machine); no BOB-E code other than 2000, no
   registry error.
   Sizes: manifests 176 MB (731 packuments, 53 per-version docs, index.json), blobs 36 MB
   (289 tarballs). Opam snapshot not recorded (no ocaml suite was run; the record mode fetches it
   on first use).

## Owner refresh command
See tests/fixtures/registry/README.md ("Recording"). In short:
```
go build -o /tmp/regmirror ./tools/regmirror/cmd/regmirror
/tmp/regmirror -manifests ~/.cache/bob-test/registry-manifests -blobs ~/.cache/bob-test/registry-blobs \
    -record-npm -record-opam -opam-commit af5ddb0addf6e75660ee9d7485b27425459d1d66 -listen 127.0.0.1:4873 &
BOB_TEST_REGISTRY_URL=http://127.0.0.1:4873 go test -count=1 -timeout 6h ./tests/eco/npm/... ./tests/eco/gen/... ./tests/eco/ocaml/...
kill %1
```
To refresh from scratch, delete both cache directories first.

## npm suite results in the recording run (not registry-related; for the owners of pkg/npm)
FAIL: TestRNPM007aFastifyBundle and TestRNPM009aBunApp (bin under `env -i` exits 1),
TestRNPM007aReactAndSvelteKit/sveltekit (output differs between two builds),
TestRNPM008aGeneratedFilesGolden (goldens tests/goldens/native/npm/* missing; package.json not
2-space indented), TestRNPM003aIsolatedLayoutAndHoisted (no output path in package.finish),
TestRNPM005aQuirkTestsPassInFixture (quirk tests exit 7), TestRNPM004aScriptsDeniedByDefault
(raw store paths in `bob run --json`), TestRNPM010a* (`bob lock` BOB-E-7002 permission denied on
the test's state dir). No request reached the mirror's error path (no 502, no missing document).

## NEEDS
1. Owner: the snapshot is not in git (A141). Each machine running the npm/gen/ocaml suites needs
   the cache directories (copy them, or run the refresh command).
2. Decision numbering: A141 was free when written; check for collisions with parallel units.
