---
title: Releases and downloads
section: Server
order: 10
summary: How the server publishes releases, serves their assets, tags their images and makes them installable.
---

Releases are declared in `bob.go` and published by pipelines (see
[Releases](../04-guides/07-releases.md)). This page covers the server side: what publishing
does, where assets are downloaded from, and visibility.

## Publishing

When a build of a release pipeline succeeds, the release controller:

1. creates the release record `<project>/<name>@<version>`; a version that exists already fails
   the pipeline (`releases are immutable`);
2. copies the assets (tarballs, AppImages) from staging to `rel/` in the artifacts bucket;
3. for a public release, marks every path and image object of the release public;
4. tags the images `<project>/<name>:<version>` and moves `latest`;
5. marks the release `published`.

`ReleaseService/ListReleases` (and the project page of the web UI) shows the result:

```text
{"releases":[{"id":"hello-oci.0000000001", "projectId":"hello-oci", "name":"hello-release", "version":"1.0.0",
  "visibility":"VISIBILITY_PRIVATE", "state":"RELEASE_STATE_PUBLISHED",
  "closures":[{"platform":"linux-amd64", …}, {"platform":"linux-arm64", …}],
  "assets":[{"file":"hello-appimage-1.0.0-aarch64.AppImage", …, "url":"http://127.0.0.1:18443/r/hello-oci/hello-release/1.0.0/hello-appimage-1.0.0-aarch64.AppImage"}, …],
  "images":[{"repository":"hello-oci/hello-release", "tag":"1.0.0", …}]}]}
```

The version comes from the build: an explicit version (API, UI), else the pipeline's tag
without a leading `v` (`v1.0.0` → `1.0.0`), else `0.0.0-dev`.

## Downloads

| URL | Answer |
|---|---|
| `GET /r/<project>/<release>/<version>/<file>` | the asset, with `ETag: "<sha256>"` and caching headers |
| `GET /r/<project>/<release>/latest/<file>` | a redirect to the highest version |

Public releases download anonymously. Private ones need a token as HTTP Basic password (any
user name) or `Authorization: Bearer`; without permission the answer is 404, so private
releases do not leak their existence:

```text
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18443/r/hello-oci/hello-release/1.0.0/hello-tar-1.0.0-linux-arm64.tar.gz
404
$ curl -s -u "bob:$TOKEN" http://127.0.0.1:18443/r/hello-oci/hello-release/1.0.0/hello-tar-1.0.0-linux-arm64.tar.gz | sha256sum
7409578fbafa692adb63025d5ac168803415ce478ffce6366ef50dfc2706bdc3  -
```

All bytes stream through the orchestrator; there are no redirects to the storage backend.

## Installs

`bob install <project>/<release>@<version>` resolves the release on the registry, downloads
the closure for its platform from the cache, and writes shims (see
[Releases](../04-guides/07-releases.md#install-with-bob-install)). Releases without closures
cannot be installed.

## Visibility, deletion and retention

Visibility (public or private) starts from `ReleaseOpts.Public` and can be changed later
(`UpdateRelease`); making a release private takes effect within about 30 seconds.
`DeleteRelease` removes its assets and tags; the version is never reused. Release artifacts
are never collected by cache retention while the release exists.

## Next steps

- [Logs and the web UI](11-logs-and-ui.md).
- [OCI registry](09-oci-registry.md).
