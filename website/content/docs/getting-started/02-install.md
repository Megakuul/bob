---
title: Install
section: Getting started
order: 2
summary: Build bob from a source checkout with the bootstrap script.
placeholder: true
---

There is no released binary yet. Build bob from a checkout:

```bash
# needs Go, curl and zstd
$ git clone https://github.com/megakuul/bob
$ cd bob && scripts/bootstrap.sh
```

> [!WARNING]
> bob needs unprivileged user namespaces. On hosts that block them, bob refuses to start and
> prints the exact fix.

## Requirements

| Tool | Why |
|---|---|
| Go | builds the first `bob` |
| curl | fetches the pinned static Nix |
| zstd | unpacks it |

## Check the result

```yaml
# bob.lock is JSON, but CI configs are often YAML
steps:
  - run: bob build //...
    timeout: 30
```
