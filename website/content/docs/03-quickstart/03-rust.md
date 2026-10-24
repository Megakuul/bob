---
title: Rust quickstart
section: Quickstart
order: 3
summary: Build, test and run a tiny Rust program with bob - one crate, compiled with rustc from nixpkgs, without Cargo.
---

In this quickstart you build a one-file Rust program with a unit test. bob compiles it with
rustc from nixpkgs and writes a `Cargo.toml` so rust-analyzer and `cargo` keep working.

You need [bob installed](../02-install/02-install-bob.md). You do not need Rust installed.

## The project

```tree
📁 hello-rust/
  📄 bob.go
  📁 src/
    🦀 main.rs
```

```bash
mkdir hello-rust && cd hello-rust
git init
```

## Write the code

```rust file=src/main.rs
fn greeting(name: &str) -> String {
    format!("hello, {name}")
}

fn main() {
    println!("{}", greeting("bob"));
}

#[cfg(test)]
mod tests {
    #[test]
    fn greets() {
        assert_eq!(super::greeting("bob"), "hello, bob");
    }
}
```

## Describe the build

```go file=bob.go
//go:build bob

package hellorust

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var Hello = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{Name: "hello"})
})

var Test = bob.Define(func(p *bob.P) *rust.TestSet {
	return rust.Test(p, rust.TestOpts{Crate: Hello(p)})
})
```

The crate root is `src/main.rs` by default. Without `Name`, the crate is named after its
directory.

## Lock, build, test, run

```bash run
bob lock
bob build
```

```text expect
…
  + nixpkgs  nixpkgs   …
…
built   //:hello
bob: 1 built
```

```bash run
bob test
```

```text expect
PASS    //:hello_test (…s)
bob: 1 passed, 0 failed, 0 cached
```

```bash run
bob run //:hello
```

```text expect
hello, bob
```

## Crates from crates.io

Add a dependency with a Cargo version requirement and run `bob lock`. bob resolves it like
Cargo, pins every crate with its checksum in `bob.lock`, and compiles each crate as its own
cached step:

```go
rust.Binary(p, rust.BinOpts{
	Name: "hello",
	Deps: []rust.Dep{rust.Crate("anyhow", "1"), rust.Crate("serde", "1", rust.Features("derive"))},
})
```

## Editor support

bob writes `Cargo.toml`, `Cargo.lock` and `.cargo/config.toml` for tooling. Do not edit them;
bob never runs Cargo to build. `cargo` and rust-analyzer are in the dev shell:

```bash run
bob dev -- cargo --version
```

```text expect
bob: dev shell default (… rustc 1.95.0, cargo 1.95.0, rust-analyzer … · …
cargo 1.95.0 …
```

## Next steps

- Libraries, features, build scripts, C interop: [bob.go reference](../04-guides/01-bob-go-reference.md#rust).
- [Multi-language projects](../04-guides/03-multi-language.md): a Rust static library in a Go program.
- Ship it: [Images, tarballs and AppImages](07-outputs.md).
