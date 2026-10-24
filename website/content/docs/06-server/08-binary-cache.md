---
title: Binary cache
section: Server
order: 8
summary: How bob login turns the server into a Nix binary cache for your machine, and how stock Nix can use it too.
---

Every orchestrator serves a Nix binary cache of what the fleet built. This page shows how
clients use it and who may read what.

## With bob

`bob login` configures the cache in bob's own Nix configuration. Nothing outside bob's
directories changes:

```text
$ grep -E 'substituters|trusted' ~/.local/share/bob/etc/nix/nix.conf
substituters = https://cache.nixos.org http://127.0.0.1:18443/nix
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= bob-1:kVIi3ViYAyWs1G+TUjpoYJlYV9BVhNNsgEAv0yvozkQ=
```

The token goes to `~/.local/share/bob/etc/nix/netrc` (mode 0600). From then on, local builds
download what the server already built: on a fresh machine, a local `bob build` of a package
that a remote build produced printed `bob: nothing to do` and downloaded the output instead of
building it. `bob logout` removes the entries again.

Before using the cache, bob checks the token once (`AuthService/WhoAmI`): an expired login is
`BOB-E-4008` before anything is built; an unreachable server gives a warning, and bob builds
without it.

## Visibility

- Cache paths belong to the projects whose builds produced them. Reading a path needs
  `CacheService/GetPath` on one of those projects; for everyone else the path does not exist
  (404).
- Paths of a public release can be read anonymously.
- Only bob-built outputs are stored. Paths available from `cache.nixos.org` are never copied.
- Narinfos are signed with the server's ed25519 key (`<server name>-<n>`). Keys rotate:
  `SettingsService/CreateSigningKey` adds a new key, `RetireSigningKey` re-signs and drops the
  old one.

```text
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18443/nix/0ckriank13l809scy8n23alw352hnr70.narinfo
404
$ curl -s -u "bob:$TOKEN" http://127.0.0.1:18443/nix/0ckriank13l809scy8n23alw352hnr70.narinfo | head -3
StorePath: /nix/store/0ckriank13l809scy8n23alw352hnr70-go-link-hello
URL: nar/0ckriank13l809scy8n23alw352hnr70.nar.zst
Compression: zstd
```

## With stock Nix

The cache speaks the standard Nix binary cache protocol at `<base URL>/nix`. To use it from a
plain Nix installation, add it as a substituter with the server's public key (shown in the
web UI and by `AuthService/GetServerInfo`), and put a token in a netrc file:

```text
substituters = https://cache.nixos.org https://bob.example.com/nix
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= bob-1:<key>
netrc-file = /etc/nix/bob-netrc
```

```text
machine bob.example.com login bob password <token>
```

Any token works as the password: a login's access token, a `download`-scope service-account
token, or an `api` one. The paths are bob's input-addressed store paths, so a plain Nix finds
them only when it asks for exactly those paths (for example when copying a closure from bob).

## Next steps

- [OCI registry](09-oci-registry.md).
- [Releases and downloads](10-releases-and-downloads.md).
