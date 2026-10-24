---
title: OCaml quickstart
section: Quickstart
order: 5
summary: Build an OCaml library with an opam dependency, a native executable and a TAP test, without opam or dune installed.
---

In this quickstart you build an OCaml library that uses the `fmt` package from opam, a native
executable, and a test. bob resolves opam packages from opam-repository, pins them in
`bob.lock`, and compiles your modules with `ocamlopt` directly. It writes `.merlin` files so
ocaml-lsp and merlin see the same build.

You need [bob installed](../02-install/02-install-bob.md). You do not need OCaml, opam or dune.

## The project

```text
hello-ocaml/
├── go.mod             needed to evaluate bob.go
├── bob.go             nixpkgs pin
├── greet/             library "greet" (modules Greet.Hello, …)
│   ├── bob.go
│   └── hello.ml
├── app/               executable "hello"
│   ├── bob.go
│   └── main.ml
└── test/              test "greet_test"
    ├── bob.go
    └── test.ml
```

```bash
mkdir hello-ocaml && cd hello-ocaml
git init
```

## The library

A library takes every `.ml`/`.mli` file of its directory. Its modules are wrapped in a
namespace named after the library, as dune does: `hello.ml` becomes `Greet.Hello`.

```ocaml file=greet/hello.ml
(* Greetings. *)
let hello name = Fmt.str "hello, %s" name
```

```go file=greet/bob.go
//go:build bob

package greet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Lib is the greet library (label //greet:greet); its modules are Greet.*.
var Lib = bob.Define(func(p *bob.P) *ocaml.Lib {
	return ocaml.Library(p, ocaml.LibOpts{
		Deps: []ocaml.Dep{ocaml.Opam("fmt", "")},
	})
})
```

`ocaml.Opam("fmt", "")` is the opam package `fmt` with no version constraint; write
`ocaml.Opam("lwt", ">= 5.7")` for one. As a dependency it means the findlib library of the
same name; pick another one with `.Lib("lwt.unix")`.

## The executable and the test

```ocaml file=app/main.ml
let () =
  let name = if Array.length Sys.argv > 1 then Sys.argv.(1) else "world" in
  print_endline (Greet.Hello.hello name)
```

```go file=app/bob.go
//go:build bob

package app

import (
	"example.com/helloml/greet"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Hello is bin/hello (label //app:hello).
var Hello = bob.Define(func(p *bob.P) *ocaml.Exe {
	return ocaml.Executable(p, ocaml.ExeOpts{
		Name: "hello",
		Deps: []ocaml.Dep{greet.Lib(p)},
	})
})
```

A test is an executable that bob runs: exit status 0 is a pass, and TAP output is reported per
case. `Main` names the main module (`test.ml`):

```ocaml file=test/test.ml
let () =
  let ok = Greet.Hello.hello "bob" = "hello, bob" in
  print_endline "TAP version 14";
  print_endline "1..1";
  Printf.printf "%s 1 - hello\n" (if ok then "ok" else "not ok");
  if not ok then exit 1
```

```go file=test/bob.go
//go:build bob

package test

import (
	"example.com/helloml/greet"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// GreetTest runs test.ml (label //test:greet_test).
var GreetTest = bob.Define(func(p *bob.P) *ocaml.Test {
	return ocaml.NewTest(p, ocaml.ExeOpts{Name: "greet_test", Main: "test", Deps: []ocaml.Dep{greet.Lib(p)}})
})
```

## The root files

```go file=bob.go
//go:build bob

package helloml

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
```

```text file=go.mod
module example.com/helloml

go 1.26.0
```

## Lock, build, test, run

`bob lock` reads opam-repository at a pinned commit, solves the constraints and pins every
package's source archive with its checksum. The OCaml compiler comes from nixpkgs:

```bash run
bob lock
```

```text expect
bob.lock: 2 changes
  + nixpkgs  nixpkgs   …
  + opam     opam
```

```bash run
bob build
```

```text expect
built   //app:hello
built   //greet:greet
bob: 2 built
```

```bash run
bob test
```

```text expect
PASS    //test:greet_test (…s)
bob: 1 passed, 0 failed, 0 cached
```

```bash run
bob run //app:hello -- bob
```

```text expect
hello, bob
```

## C stubs, ppx and dune

- C stubs: list them in `LibOpts.Stubs` (`bob.Files("geom_stubs.c")`) and link bob C
  libraries with `Native`.
- An opam package that needs a system library through a `conf-*` package: map it with
  `ocaml.Conf("conf-libcurl", nixpkgs.Lib(p, "curl"))` in `Options`.
- Code that needs ppx rewriters or menhir: set `Dune: true`. bob then generates a dune file
  and runs dune inside the build step for that library.

> [!NOTE]
> OCaml builds for the host platform only; cross-compiling is refused with `BOB-E-0011`.
> opam packages with patches or extra sources are not supported yet.

## Next steps

- All OCaml options: [bob.go reference](../04-guides/01-bob-go-reference.md#ocaml).
- [Dependencies and locking](../04-guides/02-dependencies-and-locking.md).
- Ship it: [Images, tarballs and AppImages](07-outputs.md).
