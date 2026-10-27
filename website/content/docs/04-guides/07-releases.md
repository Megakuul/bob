---
title: Releases
section: Guides
order: 7
summary: Declare a release, build it locally with a version, publish it through a bob server pipeline, and install it with bob install.
---

A release is a versioned, immutable set of artifacts: installable closures, OCI images,
tarballs and AppImages. You declare it in `bob.go`, a bob server publishes it, and users
download it or install it with `bob install`. This guide walks through all three steps.

## Declare a release

`release.New` groups outputs under a name. The
[outputs quickstart](../03-quickstart/07-outputs.md) declares this one in `deploy/bob.go`
(label `//deploy:hello-release`; the program comes from the typed slot `Hello`, bound by
`cmd/hello/bob.go`):

```go
var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "hello-release", release.ReleaseOpts{
		Closures:  []bob.Executable{Hello.Get(p)},
		Images:    []*oci.Image{Image(p)},
		Tarballs:  []*tarball.Tarball{Tar(p)},
		AppImages: []*appimage.Bundle{Bundle(p)},
	})
})
```

| Field | Meaning |
|---|---|
| `Closures` | programs that `bob install` can install; built for every platform in `Platforms` (default linux-amd64 and linux-arm64) |
| `Images`, `Tarballs`, `AppImages` | artifacts users download or pull |
| `Public` | `true`: installable and downloadable without logging in |
| `Notes` | a Markdown file shown in the web UI |

## Build a release locally

Build the release package with a version. The output is the manifest `release.json`, which
lists every artifact with its platform, store path, file name and SHA-256:

```bash
bob build //deploy:hello-release --version 1.0.0 --print-paths
```

Without `--version`, bob uses `0.0.0-dev`. A local build publishes nothing.

## Publish it through a pipeline

Publishing is the server's job. A pipeline that lists the release in `Releases` (by label
pattern; one that matches no release is a definition error) publishes it after its build
succeeds:

```go
var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnTag("v*")},
	Releases: []bob.Pattern{"//deploy:hello-release"},
})
```

On a server, a pushed tag `v1.0.0` starts the pipeline and the release gets version `1.0.0`.
A run started from the API or the web UI can set the version explicitly. When the build
succeeds, the server:

1. creates the release record; a version that already exists fails (`releases are
   immutable`);
2. copies the assets (tarballs, AppImages) to the release's download location;
3. tags the images: `<project>/<release name>:<version>`, and moves `latest`;
4. marks the release `published`.

In a test run on the documentation host, a pipeline run with version `1.0.0` published
`hello-oci/hello-release@1.0.0` with closures for linux-amd64 and linux-arm64, the AppImage and
tarball as downloadable assets, and the image as `hello-oci/hello-release:1.0.0`.

> [!NOTE]
> `bob pipeline run --remote` is not implemented yet. Start release pipelines from a forge tag
> push, the web UI or the API (`BuildService/CreateBuild` with `pipeline` and `version`); see
> [CI and remote builds](../06-server/07-ci-and-remote-builds.md).

## Download assets

Every asset has a stable URL:

```text
<server>/r/<project>/<release>/<version>/<file>
<server>/r/<project>/<release>/latest/<file>        (redirects to the highest version)
```

Private releases need a token (any API, download or service-account token as the password of
HTTP Basic auth, or `Authorization: Bearer`); without one the server answers 404:

```bash
curl -fsSLO -u "bob:$TOKEN" https://bob.example.com/r/acme/hello-release/1.0.0/hello-tar-1.0.0-linux-arm64.tar.gz
```

Images are pulled from the server's [OCI registry](../06-server/09-oci-registry.md).

## Install with bob install

`bob install` installs the closure of a release for the host platform, without a checkout and
without building anything. It writes small shims for the release's programs into
`~/.local/bin`:

```text
$ bob install hello-oci/hello-release@1.0.0
installed hello-oci/hello-release 1.0.0 (generation 1): hello → ~/.local/bin
$ hello
hello, bob
$ bob list
hello-oci/hello-release   1.0.0   default   hello
```

A reference is `[<registry>:]<project>/<release>[@<version>|@latest]`. Every change is one
atomic "generation":

| Command | Effect |
|---|---|
| `bob install <ref> [--as <name>] [--bin-dir <dir>]` | install or add a release |
| `bob list` / `bob list --available <project>` | installed releases / releases on the registry |
| `bob upgrade [--minor-only]` | move every entry to its newest version |
| `bob rollback [--to N]` | go back to the previous (or N-th) generation |
| `bob uninstall <ref>` | remove an entry |

Public releases install anonymously. Private ones need `bob login` (or `BOB_TOKEN`). The
first server you log in to becomes the registry `default`; add more with
`bob registry add <name> <url>`. A release without closures (images only) cannot be installed
(`BOB-E-0009`).

## Next steps

- [Releases and downloads on the server](../06-server/10-releases-and-downloads.md).
- [Pipelines](06-pipelines.md).
- [CLI reference: bob install](../05-cli/01-commands.md#bob-install).
