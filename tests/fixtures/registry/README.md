# Registry mirror manifests

Input of `tools/regmirror` (HARNESS.md §4.3): the offline npm registry and opam repository
that the gate runs use instead of the network. Manifests are small JSON documents committed
here; blobs (tarballs, archives) are large and live in a blob cache outside the repository.
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

Status: no manifests are committed yet. The first `-refresh-registry` run (manual, online)
populates `npm/` and `index.json` for the graphs listed in `tests/eco/npm/graphs_test.go` and
`tests/eco/ocaml/corpus_test.go`; until then the resolution tests are red.
