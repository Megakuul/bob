---
title: Install bob
section: Install
order: 2
summary: Build bob from source, check the installation with bob doctor, set up shell completion and upgrade later.
---

You build `bob` from a checkout of its repository, put the binary on your `PATH`, and check
that it can create its store. It takes a few minutes.

## Release binaries

Release binaries are not published yet. When they are, installing bob will be one download
of a single static file. Until then, build it from source as described below.

## Build bob from source

You need a checkout of the bob repository, Go (at least the version in its `go.mod`,
currently 1.26.9), `curl` and `zstd`.

1. Fetch the static Nix that bob embeds. The script checks every download against a pinned
   hash:

   ```bash
   sh scripts/fetch-nix.sh
   ```

2. Build `bob` and install it into `~/.local/bin`:

   ```bash
   mkdir -p ~/.local/bin
   CGO_ENABLED=0 go build -trimpath -o ~/.local/bin/bob ./cmd/bob
   ```

3. Make sure `~/.local/bin` is on your `PATH`, then check the version:

   ```bash
   bob version
   ```

   ```text
   bob v0.0.0-dev
   nix 2.34.9
   nixpkgs nixos-26.05
   ```

The binary is static. You can copy it to other machines of the same architecture.

### The self-hosted build (scripts/bootstrap.sh)

`scripts/bootstrap.sh` does the steps above and then lets that first `bob` build itself
(`bob build //cmd/bob:bob`). The last step is how bob's own CI builds bob.

> [!WARNING]
> At the time of writing, the last step fails with
> `BOB-E-9003: //cmd/bob:bob refers to nixpkgs bob-builder-17, which it must not reference`.
> The first `bob` that the script builds with Go works: it is in `.bob/bootstrap/bob` of the
> checkout. Use that binary, or the `go build` command above.

## Check the installation

`bob doctor` enters bob's namespace, unpacks the embedded Nix on first use, and verifies the
store and your configuration files:

```bash
bob doctor
```

```text
ok      namespace
ok      builder
ok      store
ok      config

bob: no problems
```

If user namespaces are blocked, you get `BOB-E-7005` with the fix for your system instead (see
[Requirements](01-requirements.md#unprivileged-user-namespaces)). If the store is damaged,
`bob doctor --repair` verifies every file and repairs what it can.

By default bob keeps its state in `~/.local/share/bob`. Set `BOB_HOME` to put it somewhere
else, for example on a bigger disk:

```bash
export BOB_HOME=/data/bob
```

## Shell completion

bob completes commands, flags and package labels. Labels come from the last evaluation of
the project (`.bob/labels`), so completion never runs your `bob.go` files.

| Shell | Add this line to |
|---|---|
| bash | `~/.bashrc`: `source <(bob completion bash)` |
| zsh | `~/.zshrc`: `source <(bob completion zsh)` |
| fish | `~/.config/fish/config.fish`: `bob completion fish \| source` |

> [!NOTE]
> The zsh and fish scripts are generated and golden-tested, but they have not been run in a
> real zsh or fish session on the build host yet.

## Upgrade bob

Pull the new source and run the build steps again (`sh scripts/fetch-nix.sh`, then
`go build`). Then:

- your projects keep working: `bob.lock` does not depend on the bob version;
- the first command in each project recompiles that project's evaluator, because the bob
  version is part of the evaluator key;
- the store stays; outputs that did not change are not rebuilt.

If you installed releases with `bob install`, their shims call bob by its absolute path. Keep
the binary at the same path, or set `BOB` to the new path.

## Uninstall

Delete the binary and bob's directories:

```bash
rm ~/.local/bin/bob
rm -rf ~/.local/share/bob ~/.config/bob ~/.local/state/bob
```

In each project, `.bob/` holds bob's local state and can be deleted too.

## Next steps

- [Go quickstart](../03-quickstart/01-go.md): your first project.
- [Dev environments](../04-guides/05-dev-environments.md): the shell hook and editor setup.
- [CLI reference](../05-cli/01-commands.md).
