---
title: Code generation quickstart
section: Quickstart
order: 6
summary: Generate Go code from a protobuf file and use it in a program - the generator runs in the sandbox, its output lands in your tree.
---

In this quickstart you generate Go code from a `.proto` file and use it in a small program.
bob runs the generator in the sandbox, at the version of your nixpkgs pin, adds the matching
runtime library, and writes the generated code into your tree so your editor sees it. GraphQL
and OpenAPI work the same way.

You need [bob installed](../02-install/02-install-bob.md). You do not need `protoc` or `buf`.

## The project

```tree
📁 hello-proto/
  📄 go.mod
  📄 bob.go
  📁 proto/
    📁 greet/
      📁 v1/
        📄 bob.go
        📄 greet.proto
  📁 cmd/
    📁 hello/
      🐹 main.go
```

```bash
mkdir hello-proto && cd hello-proto
git init
```

## The protobuf library

A protobuf library is the `.proto` files of one directory. The files need no `go_package`
option:

```proto file=proto/greet/v1/greet.proto
syntax = "proto3";

package greet.v1;

message Greeting {
  string name = 1;
}
```

```go file=proto/greet/v1/bob.go
//go:build bob

package v1

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/proto"
)

var Greet = bob.Define(func(p *bob.P) *proto.Library {
	return proto.NewLibrary(p, proto.LibraryOpts{})
})
```

## Generate Go code into the module

`connect.Go` generates protobuf (and connect) code into `gen/proto/greet/v1` of the module and
adds `google.golang.org/protobuf` at the version that matches the generator:

```go file=bob.go
//go:build bob

package hello

import (
	greetv1 "example.com/hello/proto/greet/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:      "example.com/hello",
		Go:        "1.26",
		Generated: []golang.Generated{connect.Go(greetv1.Greet(p), connect.GoOpts{})},
	})
})

var Hello = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/hello", golang.BinaryOpts{})
})
```

The program imports the generated package:

```go file=cmd/hello/main.go
package main

import (
	"fmt"

	greetv1 "example.com/hello/gen/proto/greet/v1"
)

func main() {
	g := &greetv1.Greeting{Name: "bob"}
	fmt.Println("hello,", g.GetName())
}
```

```text file=go.mod
module example.com/hello

go 1.26.0
```

## Lock, build, run

```bash run
bob lock
bob build
```

```text expect
…
  + go       google.golang.org/protobuf   …
…
built   //:gen-proto-greet-v1
built   //cmd/hello:hello
…
```

The generated tree is a package of its own, `//:gen-proto-greet-v1`. bob builds it first and
stores the output by content: a change to the `.proto` file that generates the same code (a
comment) rebuilds nothing else.

```bash run
bob run //cmd/hello:hello
```

```text expect
hello, bob
```

## The generated code

bob writes the generated files read-only into the module, with a `.gitignore` that keeps them
out of git:

```bash run
ls gen/proto/greet/v1
bob status
```

```text expect
greet.pb.go
fresh    gen/proto/greet/v1/   //:gen-proto-greet-v1
fresh    bob.lock
```

After a clone, `bob gen` writes generated code without building anything else.

## Other generators

| Schema | Go | TypeScript |
|---|---|---|
| protobuf (`proto.NewLibrary`) | `connect.Go` | `connect.TS` |
| GraphQL (`graphql.NewSchema`) | `graphql.Gqlgen` | `graphql.TS` |
| OpenAPI (`openapi.NewSpec`) | `openapi.Go` | `openapi.TS` |

The [Generated code guide](../04-guides/04-generated-code.md) shows each of them.

## Next steps

- [Generated code](../04-guides/04-generated-code.md): GraphQL, OpenAPI, TypeScript and edited files.
- [bob.go reference: code generators](../04-guides/01-bob-go-reference.md#code-generators).
