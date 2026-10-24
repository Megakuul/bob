---
title: Images, tarballs and AppImages
section: Quickstart
order: 7
summary: Turn a program into an OCI image, a tarball, an AppImage and a versioned release - all ordinary bob packages.
---

In this quickstart you package the `hello` program of the [Go quickstart](01-go.md) in four
ways: an OCI image (without Docker), a tarball of its closure, an AppImage that runs on other
Linux machines, and a release that groups them under a version. Outputs work the same for
programs written in C/C++, Rust, TypeScript or OCaml.

## The project

The Go program of the [Go quickstart](01-go.md), with the outputs declared in `bob.go`:

```tree
📁 hello-ship/
  📄 go.mod
  📄 bob.go
  📁 cmd/
    📁 hello/
      🐹 main.go
```

```bash
mkdir hello-ship && cd hello-ship
git init
```

```go file=cmd/hello/main.go
package main

import "fmt"

func main() { fmt.Println("hello, bob") }
```

```text file=go.mod
module example.com/hello

go 1.26.0
```

Each output takes the program by value (`Hello(p)`), so the image, the tarball and the
AppImage contain exactly the binary that `bob build` produces:

```go file=bob.go
//go:build bob

package hello

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/appimage"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/output/tarball"
	"github.com/megakuul/bob/pkg/release"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/hello", Go: "1.26"})
})

var Hello = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/hello", golang.BinaryOpts{})
})

var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "hello-image", oci.ImageOpts{Program: Hello(p)})
})

var Tar = bob.Define(func(p *bob.P) *tarball.Tarball {
	return tarball.New(p, "hello-tar", tarball.TarballOpts{Program: Hello(p), Kind: tarball.Closure})
})

var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "hello-appimage", appimage.BundleOpts{Program: Hello(p)})
})

var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "hello-release", release.ReleaseOpts{
		Closures:  []bob.Executable{Hello(p)},
		Images:    []*oci.Image{Image(p)},
		Tarballs:  []*tarball.Tarball{Tar(p)},
		AppImages: []*appimage.Bundle{Bundle(p)},
	})
})
```

> [!NOTE]
> A package is identified by its label. Declaring the same binary again elsewhere with
> different options fails with `package //cmd/hello:hello is already defined`. Declare outputs
> next to the program, or call the function that declares it, as here.

## Build them

```bash run
bob lock
bob build //:hello-image //:hello-tar //:hello-appimage --print-paths
```

```text expect
…
built   //:hello-appimage
built   //:hello-image
built   //:hello-tar
built   //cmd/hello:hello
bob: 4 built
//:hello-appimage …/nix/store/…-appimage-hello-appimage
//:hello-image …/nix/store/…-oci-image-hello-image
//:hello-tar …/nix/store/…-tarball-hello-tar-linux-…
```

The image has one manifest per platform. bob builds the Go program for the other
architecture too (Go, C/C++ and Rust cross-compile; OCaml does not).

## The AppImage

The AppImage is one executable file. It carries the program's closure and runs on Linux
machines without bob or Nix:

```bash run
cp "$(bob build //:hello-appimage --print-paths | awk '/^\/\// {print $2}')"/*.AppImage hello.AppImage
chmod +x hello.AppImage
./hello.AppImage
```

```text expect
hello, bob
```

`BOB_ENTRY=<name>` picks another entry point of a bundle with several programs
(`BundleOpts.Entries`); `BOB_APPIMAGE_DEBUG=1` prints how the launcher starts the program;
`BOB_APPIMAGE_MODE` overrides the launcher mode (`BundleOpts.Mode`).

## The tarball

`tarball.Closure` packs the program and every store path it needs under `nix/store/`, plus a
`bob/` directory with the closure roots and licence data:

```bash run
tar tzf "$(bob build //:hello-tar --print-paths | awk '/^\/\// {print $2}')"/*.tar.gz | head -3
```

```text expect
bob/
bob/licences.json
bob/roots
```

`tarball.Static` packs a single static executable instead. bob refuses it with `BOB-E-1022`
when the program is not static; Go programs from nixpkgs' Go reference `iana-etc` data, so
use `tarball.Closure` for them.

## The OCI image

The image output is a standard OCI image layout directory (`index.json`, `oci-layout`,
`blobs/`):

```bash run
ls "$(bob build //:hello-image --print-paths | awk '/^\/\// {print $2}')"
```

```text expect
blobs
index.json
oci-layout
```

Copy it into a container engine with any tool that reads OCI layouts. With podman:

```bash
podman pull oci:"$(bob build //:hello-image --print-paths | awk '/^\/\// {print $2}')"
podman run --rm <image id>
```

```text
Writing manifest to image destination
247170577a30bd5a20788d246247ec231ff2fd3f83af68dfcf1cef40fa0dd09a
hello, bob
```

Other tools that read OCI image layouts (skopeo, crane) take the same path. Images run as user `65534` (nobody) by default
and include CA certificates and time zone data; see `oci.ImageOpts` for entrypoint, ports,
environment and layer limits.

> [!NOTE]
> The podman commands were checked with podman 5.8 as root with the `vfs` storage driver. The
> registry of `bob-server` serves the same images to `podman pull`; see
> [OCI registry](../06-server/09-oci-registry.md).

## The release

A release is built with a version. Its output is a manifest, `release.json`, that lists every
artifact for every platform:

```bash run
bob build //:hello-release --version 1.0.0 --print-paths
cat "$(bob build //:hello-release --version 1.0.0 --print-paths | awk '/^\/\// {print $2}')"/release.json | head -12
```

```text expect
built   //:hello-release
…
{
  "artifacts": [
    {
      "entrypoints": {
        "hello": "bin/hello"
      },
      "kind": "closure",
      "name": "hello",
      "platform": "linux-amd64",
```

Without `--version`, bob uses `0.0.0-dev` and prints a note. Publishing a release (making it
downloadable and installable with `bob install`) is the server's job; see
[Releases](../04-guides/07-releases.md).

## Next steps

- [Releases guide](../04-guides/07-releases.md): versions, visibility, publishing.
- Output options: [bob.go reference](../04-guides/01-bob-go-reference.md#outputs-and-releases).
- [Pipelines](../04-guides/06-pipelines.md): build all of this in CI.
