---
title: OCaml quickstart
section: Quickstart
order: 5
summary: Build a tiny OCaml library, an executable and a test with bob, without opam or dune installed.
---

In this quickstart you build an OCaml library module, a native executable that uses it, and a
test. bob compiles with `ocamlopt` from nixpkgs and writes `.merlin` files for ocaml-lsp.

You need [bob installed](../02-install/02-install-bob.md). You do not need OCaml, opam or dune.

## The project

```tree
📁 hello-ocaml/
  📄 bob.go
  🐫 hello.ml
  🐫 main.ml
  🐫 test.ml
```

```bash
mkdir hello-ocaml && cd hello-ocaml
git init
```

## Write the code

`hello.ml` is the library. Its module is wrapped in the library's namespace, as dune does, so
other code calls it `Greet.Hello`:

```ocaml file=hello.ml
let greeting name = "hello, " ^ name
```

```ocaml file=main.ml
let () = print_endline (Greet.Hello.greeting "bob")
```

`test.ml` is a test: exit status 0 means pass, and TAP output is reported per case.

```ocaml file=test.ml
let () =
  let ok = Greet.Hello.greeting "bob" = "hello, bob" in
  Printf.printf "TAP version 14\n1..1\n%s 1 - greeting\n" (if ok then "ok" else "not ok");
  if not ok then exit 1
```

## Describe the build

All three share one directory, so each package names its files:

```go file=bob.go
//go:build bob

package helloml

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var Greet = bob.Define(func(p *bob.P) *ocaml.Lib {
	return ocaml.Library(p, ocaml.LibOpts{Name: "greet", Srcs: bob.Files("hello.ml")})
})

var Hello = bob.Define(func(p *bob.P) *ocaml.Exe {
	return ocaml.Executable(p, ocaml.ExeOpts{Name: "hello", Srcs: bob.Files("main.ml"), Deps: []ocaml.Dep{Greet(p)}})
})

var Test = bob.Define(func(p *bob.P) *ocaml.Test {
	return ocaml.NewTest(p, ocaml.ExeOpts{Name: "hello_test", Main: "test", Srcs: bob.Files("test.ml"), Deps: []ocaml.Dep{Greet(p)}})
})
```

## Lock, build, test, run

```bash run
bob lock
bob build
```

```text expect
…
  + nixpkgs  nixpkgs   …
…
bob: 2 built
```

```bash run
bob test
bob run //:hello
```

```text expect
PASS    //:hello_test (…s)
bob: 1 passed, 0 failed, 0 cached
hello, bob
```

## opam packages

Add an opam package with a version formula and run `bob lock`. bob solves the constraints
against opam-repository at a pinned commit and pins every source archive in `bob.lock`:

```go
ocaml.Library(p, ocaml.LibOpts{
	Name: "greet",
	Srcs: bob.Files("hello.ml"),
	Deps: []ocaml.Dep{ocaml.Opam("fmt", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix")},
})
```

> [!NOTE]
> OCaml builds for the host platform only (`BOB-E-0011` for others). opam packages with
> patches or extra sources are not supported yet. Libraries that need ppx or menhir use
> `Dune: true`.

## Next steps

- All OCaml options: [bob.go reference](../04-guides/01-bob-go-reference.md#ocaml).
- Ship it: [Images, tarballs and AppImages](07-outputs.md).
