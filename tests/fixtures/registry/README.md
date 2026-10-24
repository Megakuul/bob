# Registry mirror manifests

Input of `tools/regmirror` (HARNESS.md §4.3): the offline npm registry and opam repository
that the gate runs use instead of the network. Manifests are JSON documents (committed
here while small, else outside git: see below); blobs (tarballs, archives) are large and live in a blob cache outside the repository.
Recapturing is a manual, online step (`-refresh-registry`); refreshed manifests are reviewed
like goldens. Protected after Gate H.

```
index.json                                 {"v":1,"captured":"<date>","npm":{"snapshot":"<date>"},
                                            "opam":{"commit":"<40 hex>"}}
npm/packuments/<name>.json                 abbreviated packument (Accept: application/vnd.npm.install-v1+json);
                                           scoped names escape "/" as "%2f"; every dist.tarball is
                                           "@REGISTRY@/<name>/-/<file>.tgz"
npm/versions/<name>/<version>.json         per-version document (os, cpu, libc), only for packages that have os/cpu
```

Blob cache (`$BOB_REGISTRY_BLOBS`, default `$XDG_CACHE_HOME/bob-test/registry-blobs`):

```
npm/<name>/-/<file>.tgz                    tarball (integrity as in the packument)
opam/repository/<commit>.tar.gz            opam-repository snapshot of index.json's commit
opam/cache/<alg>/<xx>/<hash>               opam archive cache entry (alg sha256|sha512, xx = first two hex digits)
```

`@REGISTRY@` is replaced at serve time by `http://<host>/npm`, so manifests do not depend on a
port. Documents are served with an ETag. Routes and validation rules: `tools/regmirror`.

How bob is pointed at the mirror (test-only environment, decision of unit T4b):
`BOB_NPM_REGISTRY=<url>/npm/`, `BOB_OPAM_REPOSITORY=<url>/opam/repository/<commit>.tar.gz`,
`BOB_OPAM_CACHE_URL=<url>/opam/cache`. A harness may instead set `BOB_TEST_REGISTRY_URL` to a mirror
it started itself.

## Where the snapshot lives (decision A141)

Manifests are committed here only while the snapshot stays below 150 MB. The npm packuments of
the graphs and fixtures are larger (abbreviated packuments of popular packages are 1-10 MB each),
so the snapshot is kept outside git; the harness (`tests/eco/internal/ecotest.RegistryManifests`)
looks for the manifests in this order:

1. `$BOB_REGISTRY_MANIFESTS`,
2. this directory, when it holds an `index.json` (a committed snapshot),
3. `$XDG_CACHE_HOME/bob-test/registry-manifests` (default `~/.cache/bob-test/registry-manifests`).

Blobs stay in `$BOB_REGISTRY_BLOBS` (default `~/.cache/bob-test/registry-blobs`).

## Recording (manual, online; owner)

`tools/regmirror` records through: run it with `-record-npm` (and `-record-opam`) and run the
suites against it. Every miss is fetched once from upstream and kept: npm abbreviated packuments
(`Accept: application/vnd.npm.install-v1+json`) with `dist.tarball` rewritten to `@REGISTRY@/…`,
per-version documents when asked for (os/cpu packages), tarballs into the blob cache; opam: the
GitHub archive of the commit and opam.ocaml.org cache entries. Existing files are never
re-fetched, so a recording can be resumed and is extended only by what a run actually loads.
`index.json` is written at start (capture/snapshot date kept once set).

```
go build -o /tmp/regmirror ./tools/regmirror/cmd/regmirror
/tmp/regmirror -manifests ~/.cache/bob-test/registry-manifests -blobs ~/.cache/bob-test/registry-blobs \
    -record-npm -record-opam -opam-commit af5ddb0addf6e75660ee9d7485b27425459d1d66 \
    -listen 127.0.0.1:4873 &
BOB_TEST_REGISTRY_URL=http://127.0.0.1:4873 go test -count=1 -timeout 6h \
    ./tests/eco/npm/... ./tests/eco/gen/... ./tests/eco/ocaml/...
kill %1
```

The resolution tests alone (`-run TestRNPM001a ./tests/eco/npm/`) record every packument that
bob and the npm oracle load for the ten graphs; the build tests add the tarballs and the
per-version documents. To refresh, delete the two cache directories and record again (a new
snapshot date; goldens that depend on resolved versions are reviewed again).
