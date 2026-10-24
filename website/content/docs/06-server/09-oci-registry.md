---
title: OCI registry
section: Server
order: 9
summary: Pull bob-built images from the server's registry with podman or any OCI client.
---

Orchestrators serve a pull-only OCI registry for the images the fleet built. This page shows
how to log in and pull.

## Repositories and tags

| Image from | Repository | Tags |
|---|---|---|
| a build (`bob build --remote` of an `oci.NewImage` package) | `<project>/<image name>` | the build id, e.g. `b-01M5PXMCB6NJN6Z6F0ZGG2HVGN` |
| a release | `<project>/<release name>` | the version, e.g. `1.0.0`, and `latest` |

Build tags are visible to the project's members only; release tags of public releases are
public. Pushing is refused: only the server's promotion writes images.

## Log in and pull

Use any token as the password (user name ignored). The registry speaks the standard token
flow (`/v2/` answers 401 with a `Bearer` realm at `/oci/token`). Over plain HTTP, tell the
client so:

```text
$ echo "$TOKEN" | podman login --tls-verify=false --username bob --password-stdin 127.0.0.1:18443
Login Succeeded!
$ podman pull --tls-verify=false 127.0.0.1:18443/hello-oci/hello-image:b-01M5PXMCB6NJN6Z6F0ZGG2HVGN
…
Writing manifest to image destination
6785740d298626f9b9ce9eeb50f99aea999040ecb2e1eaf2e6b97783391108e0
$ podman run --rm 127.0.0.1:18443/hello-oci/hello-image:b-01M5PXMCB6NJN6Z6F0ZGG2HVGN
hello from oci, registry
```

Behind a TLS proxy, drop `--tls-verify=false`. Kubernetes and docker use the same credentials
(a `download`-scope service-account token is the usual choice for pull secrets).

> [!WARNING]
> A remote build of an image currently fails promotion with `BOB-E-4012 … artifact
> verification` when the program in the image was already published by an earlier build (the
> server log says the image "contains the store hash …, which is not in this job's closure").
> An image whose program is new in the same build is not affected, and in the test run the
> image of a release pipeline was published although its program had been published before.

## Next steps

- [Releases and downloads](10-releases-and-downloads.md).
- [Images, tarballs and AppImages](../03-quickstart/07-outputs.md).
