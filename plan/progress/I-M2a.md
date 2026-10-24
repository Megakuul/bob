# I-M2a progress (M2.1 drv/nar, then M2.4 fetch)

## done
- **M2.1** `pkg/engine/drv` (aterm.go, drv.go, json.go, path.go) and `pkg/engine/nar` (nar.go + nar_test.go).
  - Green: TestRNIX006GoldenATerm (all 5 Nix goldens, incl. structured attrs), TestRNIX006StoreNames,
    TestRNIX006FixedPathsAgainstNix (vs host nix 2.34.8), TestRNIX006OutputPathsChangeWithEnv.
  - Extra evidence (scratch test, removed): every one of the 130,458 `.drv` files in the host's
    /nix/store parses, prints back byte-identical, and recomputes to the same output paths and drv
    path (Finalize + Path, recursive Modulo through the store), 67 s, aarch64 host, 2026-10-22.
  - nar.Write equals `nix-store --dump` (unit test); 2x faster after dropping per-call 64 KiB buffers.
  - Still red, not this unit's: TestRNIX006RecomputeHello (gets past fetch now; red at nixidx.Open),
    TestRNIX006CrossCheckExamples, R-NIX-001, R-NIX-003 (need `bob build`).
  - TestRNIX006ATermEscapes fails: see TEST PROBLEM.
- **M2.4** `pkg/engine/fetch` (fetch.go, client.go, archive.go + fetch_test.go).
  - Green with the real store: TestRNIX014TamperedByte, TestRNIX014NotFoundAndFailures,
    TestRNIX014HTTPAndSizeCap, TestRNIX014UnsafeArchives (incl. 10^6+1 entries),
    TestRNIX014ArchiveFormats (tar, tar.gz, tgz, tar.bz2, tar.xz, tar.zst, zip, crate),
    TestRNIX008FetchedPathNeedsNoNetwork. Fetching real nixpkgs (GitHub tarball) through
    Resolve+Ensure works (TestRNIX006RecomputeHello reaches nixidx.Open).
  - Substitution verified by hand: Ensure of hello-2.12.3.tar.gz (flat sha256) with a dead origin
    URL was realised from cache.nixos.org at the FOD path.
  - Red, need the pipeline: TestRNIX008NoUserFixedOutputDerivations, R-FW-063 tests in tests/core.

## next
- nothing in this chain; waiting for review/commit.

## files
- pkg/engine/drv/{aterm.go,drv.go,json.go,path.go}
- pkg/engine/nar/{nar.go,nar_test.go}
- pkg/engine/fetch/{fetch.go,client.go,archive.go,fetch_test.go}
- go.mod/go.sum (+ github.com/ulikunitz/xz v0.5.17, github.com/klauspost/compress v1.20.3)
- ATTRIBUTION.md (two dependency rows)

## DECISIONS
- D-I-M2a-1: New deps `github.com/ulikunitz/xz` (BSD-3) and `github.com/klauspost/compress/zstd`
  (BSD-3 with Apache-2.0/MIT parts) for `.tar.xz`/`.tar.zst` (SPEC R-NIX-014 requires both
  formats unpacked in Go; the stdlib has neither). Rows in ATTRIBUTION.md.
- D-I-M2a-2: Job.Derivation does not add the builder store path to InputSrcs; the caller lists it
  (the Nix golden `st.drv` was evaluated with a context-free builder string, so adding it would
  break the golden). Documented on drv.Job.
- D-I-M2a-3: `__json` is written like nlohmann::json dump (sorted keys, only `"`, `\`, control
  characters escaped, raw UTF-8); non-integer numbers are refused with an error because Nix
  formats floats its own way.
- D-I-M2a-4: drv.Parse recovers Name from `name`, else the `name` structured attribute, else the
  `out` path. Modulo of a not-yet-finalised input-addressed drv finalises a copy first, so
  results never depend on the order callers finalise in.
- D-I-M2a-5: Archives are read twice: a validation pass (no writes) then extraction, so an unsafe
  archive leaves nothing on disk and the 10^6+1-entry case costs no file creation. Any `..`
  component is refused (not only escaping ones); duplicate entries (other than dir/dir) are
  refused; entries above the stripped level are refused unless they are directories; hard links
  must point to an earlier regular file of the archive and are copied (read with O_NOFOLLOW).
- D-I-M2a-6: Format is detected from magic bytes, not the URL extension (mirrors without
  extensions work).
- D-I-M2a-7: Download rules: per mirror up to 3 attempts (1 s, 4 s) for transient failures
  (network, 5xx, 408, 429); 404/410 → next mirror, no retry; a hash mismatch moves to the next
  mirror and wins as the final error (3001) over 3002/3003. Plain http needs a pinned Hash or
  NarHash; other schemes are refused (3002). Under Policy.DenyPrivate the dialer refuses
  loopback/private/link-local/unspecified/multicast addresses and the proxy env is ignored.
- D-I-M2a-8: Resolve also imports what it downloaded (the path follows from the hashes it
  returns), so the following Ensure is a validity hit and nixpkgs is downloaded once.
- D-I-M2a-9: Substitution is `nix-store --realise <path>` through store.Command (output
  discarded; a miss falls through to the origin). Scratch dirs live in `<home>/cache/fetch`.
- D-I-M2a-10: 3001 carries url/expected/got in Args and in Cause (the code table's 3001 template
  has only `label`).

## NEEDS
- store (I-M2b): `Import` takes the whole NAR in memory; nixpkgs is a few hundred MB. A streaming
  import (io.Reader per item) would cap memory. Not blocking.
- store (I-M2b): optional `Realise(ctx, paths) error` method; fetch currently builds it from
  `Store.Command`. Not blocking.
- store layout: `cache/fetch` scratch dir below the home (created by fetch on demand).

## TEST PROBLEM
- `tests/nix/drv_test.go` TestRNIX006ATermEscapes expects the inputDrvs
  `/nix/store/dddd…-a.drv` before `/nix/store/cccc…-b.drv`, i.e. sorted by name. SPEC R-NIX-006
  says "inputDrvs sorted by path", and real Nix sorts by path: the Nix-written golden
  `tests/nix/testdata/drv/fd4fcv9zpp0d0hb64wg0wf2wpqdjpgra-top.drv` lists
  `7l6q…-dep.drv`, `fdgi…-fodrec.drv`, `znd6…-fodflat.drv` (path order; name order would put
  fodflat before fodrec), and all 130,458 host drvs round-trip with path order. No single rule
  satisfies both this test and the golden; the implementation follows SPEC and Nix. The rest of
  that test's expected ATerm (escapes, other sorting) matches bob's output exactly. Fix: swap
  the two inputDrv entries in the expected string (owner decision, test is protected).
