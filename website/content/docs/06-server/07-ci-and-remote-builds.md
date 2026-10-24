---
title: CI and remote builds
section: Server
order: 7
summary: Run builds on the server from your machine or from any CI system with bob build --remote, service-account tokens and the API.
---

This page shows the ways to run work on a bob server: from your terminal, from an external CI
system, and through the API.

## From your terminal

After `bob login`, add `--remote` to `bob build` or `bob test`:

```text
$ bob build --remote //cmd/hello:hello
remote build b-01M5PXB03VHB1K6D23P07ER9N7 queued on 127.0.0.1:18443
  http://127.0.0.1:18443/p/hello-ship/builds/b-01M5PXB03VHB1K6D23P07ER9N7
downloading 1 output
built   //cmd/hello:hello

bob: 1 built
```

bob uploads a snapshot of the working tree (tracked files and untracked files that git does
not ignore), so uncommitted changes are built too. The fleet evaluates and builds, the server
promotes the results into its cache, and bob downloads them into your store. Developers never
upload build outputs themselves.

- `--no-download` skips the download.
- Ctrl-C detaches: the build goes on, and `bob cancel <build id>` stops it.
- `bob log --build <build id> <label>` prints a package's log of a server build; `-f` follows a
  running one.
- A failed server build is `BOB-E-4012` with the worker's own errors.

## From an external CI system

Any CI runner that can run bob can use the server, with a service-account token
([Users and access](05-users-and-access.md#service-accounts-and-tokens)):

```bash
export BOB_SERVER=https://bob.example.com
export BOB_TOKEN=$CI_SECRET_TOKEN       # a service-account token with build permissions
bob build --remote //...
bob test --remote //...
```

The runner needs no build tools and little disk, only bob and user namespaces. To build on the
runner itself instead and still profit from the server's cache, run `bob pipeline run ci`
with the same token: bob substitutes from the server cache, but local results are never
uploaded.

## Pipelines on the server

Pipelines run on the server when a connected forge sends an event (see
[Projects and forges](06-projects-and-forges.md)), and on demand from the web UI or the API:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"hello-oci","source":{"uploadSha256":"<sha256 of an uploaded snapshot>"},"pipeline":"release","version":"1.0.0"}' \
  https://bob.example.com/api/bob.build.v1.BuildService/CreateBuild
```

`source` is either a git source (for linked repositories) or a snapshot uploaded earlier;
`bob build --remote` uploads one, and `BuildService/GetBuild` shows its `uploadSha256`. A
build started for a pipeline ignores the pipeline's triggers.

> [!NOTE]
> `bob pipeline run <name> --remote` is not implemented yet (`BOB-E-9008`); use the API or
> the web UI to start a pipeline by hand.

Builds can be cancelled, retried and re-run from the UI or the API. Concurrency per ref follows
the pipeline's `Concurrency` setting.

## Next steps

- [Binary cache](08-binary-cache.md).
- [Releases and downloads](10-releases-and-downloads.md).
