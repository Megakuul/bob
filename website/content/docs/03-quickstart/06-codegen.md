---
title: Code generation quickstart
section: Quickstart
order: 6
summary: Generate Go and TypeScript code from protobuf (connect), GraphQL (gqlgen, graphql-codegen) and OpenAPI (oapi-codegen) schemas.
---

In this quickstart you build a Go server that speaks three API styles, all from generated
code: a connect (protobuf) service, a GraphQL endpoint and an OpenAPI REST endpoint. Then you
add a TypeScript client generated from the same schemas. bob runs the generators in the
sandbox, at the versions of your nixpkgs pin, and writes their output into your tree so your
editor sees it.

You need [bob installed](../02-install/02-install-bob.md) and should know the
[Go quickstart](01-go.md). You do not need `protoc`, `buf`, `gqlgen` or `oapi-codegen`.

## The project

```text
hello-api/
├── go.mod, bob.go          the Go module and its three generated trees
├── proto/greet/v1/         greet.proto + bob.go (protobuf library, buf lint)
├── schema/                 schema.graphqls, items.yaml + bob.go
├── graph/resolver.go       your GraphQL resolvers (hand-written)
├── cmd/server/             the server
├── cmd/client/             a Go connect client
└── web/                    a TypeScript client (connect-es, graphql-codegen)
```

```bash
mkdir hello-api && cd hello-api
git init
```

## The schemas

A protobuf library is the `.proto` files of one directory. bob finds the import root (the
nearest directory named `proto`), so `greet/v1/greet.proto` is imported as
`greet/v1/greet.proto`. The files need no `go_package` option:

```proto file=proto/greet/v1/greet.proto
syntax = "proto3";

package greet.v1;

message GreetRequest {
  string name = 1;
}

message GreetResponse {
  string greeting = 1;
}

service GreetService {
  rpc Greet(GreetRequest) returns (GreetResponse) {}
}
```

```go file=proto/greet/v1/bob.go
//go:build bob

package v1

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/proto"
)

// Greet is the greet.v1 protobuf library (label //proto/greet/v1:proto).
var Greet = bob.Define(func(p *bob.P) *proto.Library {
	return proto.NewLibrary(p, proto.LibraryOpts{})
})

// Lint runs buf lint on greet.v1 as a test (label //proto/greet/v1:lint).
var Lint = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, Greet(p)) })
```

The GraphQL schema and the OpenAPI document:

```text file=schema/schema.graphqls
type Query {
  hello(name: String!): Hello!
}

type Hello {
  greeting: String!
  length: Int!
}
```

```yaml file=schema/items.yaml
openapi: 3.0.3
info:
  title: items
  version: 1.0.0
paths:
  /items/{id}:
    get:
      operationId: getItem
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Item"
components:
  schemas:
    Item:
      type: object
      required: [id, name]
      properties:
        id:
          type: string
        name:
          type: string
```

```go file=schema/bob.go
//go:build bob

package schema

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/openapi"
)

// API is the GraphQL schema (label //schema:graphql).
var API = bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })

// Items is the OpenAPI document (label //schema:openapi-items).
var Items = bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })
```

## Generate Go code into the module

The module lists its generated trees in `Generated`. Each recipe owns one directory below
the module (`gen/proto/greet/v1`, `gen/graph`, `gen/rest`) and adds the runtime library it
needs (`connectrpc.com/connect`, `google.golang.org/protobuf`, `github.com/99designs/gqlgen`,
`github.com/oapi-codegen/runtime`) at the version that matches the generator:

```go file=bob.go
//go:build bob

package helloapi

import (
	greetv1 "example.com/helloapi/proto/greet/v1"
	"example.com/helloapi/schema"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/openapi"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Module is example.com/helloapi with three generated trees: gen/proto (protobuf and
// connect), gen/graph (gqlgen) and gen/rest (oapi-codegen).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path: "example.com/helloapi",
		Go:   "1.26",
		Generated: []golang.Generated{
			connect.Go(greetv1.Greet(p), connect.GoOpts{}),
			graphql.Gqlgen(schema.API(p), graphql.GqlgenOpts{}),
			openapi.Go(schema.Items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP}),
		},
	})
})
```

```text file=go.mod
module example.com/helloapi

go 1.26.0
```

## The Go server and client

gqlgen generates the executable schema; the resolvers stay yours:

```go file=graph/resolver.go
// Package graph holds the hand-written GraphQL resolvers.
package graph

import (
	"context"

	gqlexec "example.com/helloapi/gen/graph"
	"example.com/helloapi/gen/graph/model"
)

type Resolver struct{}

func (r *Resolver) Query() gqlexec.QueryResolver { return queryResolver{} }

type queryResolver struct{}

func (queryResolver) Hello(_ context.Context, name string) (*model.Hello, error) {
	g := "hello, " + name
	return &model.Hello{Greeting: g, Length: len(g)}, nil
}
```

The server mounts all three:

```go file=cmd/server/main.go
// Command server serves GreetService (connect), /graphql (gqlgen) and /rest (OpenAPI).
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"connectrpc.com/connect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	gqlexec "example.com/helloapi/gen/graph"
	greetv1 "example.com/helloapi/gen/proto/greet/v1"
	"example.com/helloapi/gen/proto/greet/v1/greetv1connect"
	"example.com/helloapi/gen/rest"
	"example.com/helloapi/graph"
)

type greeter struct{}

func (greeter) Greet(_ context.Context, r *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: "hello, " + r.Msg.GetName()}), nil
}

type items struct{}

func (items) GetItem(w http.ResponseWriter, _ *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rest.Item{Id: id, Name: "item " + id})
}

func main() {
	mux := http.NewServeMux()
	mux.Handle(greetv1connect.NewGreetServiceHandler(greeter{}))

	gql := handler.New(gqlexec.NewExecutableSchema(gqlexec.Config{Resolvers: &graph.Resolver{}}))
	gql.AddTransport(transport.POST{})
	mux.Handle("/graphql", gql)

	mux.Handle("/rest/", http.StripPrefix("/rest", rest.Handler(items{})))
	log.Println("listening on 127.0.0.1:8080")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", mux))
}
```

```go file=cmd/server/bob.go
//go:build bob

package main

import (
	"example.com/helloapi"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Server is bin/server (label //cmd/server:server).
var Server = bob.Define(func(p *bob.P) *golang.Binary {
	return helloapi.Module(p).Binary("./cmd/server", golang.BinaryOpts{})
})
```

A Go client through the generated connect client:

```go file=cmd/client/main.go
// Command client calls GreetService through the generated connect client.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"connectrpc.com/connect"

	greetv1 "example.com/helloapi/gen/proto/greet/v1"
	"example.com/helloapi/gen/proto/greet/v1/greetv1connect"
)

func main() {
	c := greetv1connect.NewGreetServiceClient(http.DefaultClient, "http://127.0.0.1:8080")
	res, err := c.Greet(context.Background(), connect.NewRequest(&greetv1.GreetRequest{Name: "bob"}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "greet:", err)
		os.Exit(1)
	}
	fmt.Println(res.Msg.GetGreeting())
}
```

```go file=cmd/client/bob.go
//go:build bob

package main

import (
	"example.com/helloapi"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Client is bin/client (label //cmd/client:client).
var Client = bob.Define(func(p *bob.P) *golang.Binary {
	return helloapi.Module(p).Binary("./cmd/client", golang.BinaryOpts{})
})
```

## A TypeScript client

The same schemas generate TypeScript: `connect.TS` runs protoc-gen-es, `graphql.TS` runs
graphql-codegen over your operation documents. The client is a Node program:

```text file=web/src/hello.graphql
query Hello($name: String!) {
  hello(name: $name) {
    greeting
    length
  }
}
```

```ts file=web/src/client.ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { GreetService } from "../gen/proto/greet/v1/greet_pb.js";
import type { HelloQuery, HelloQueryVariables } from "../gen/graphql/graphql.js";

const base = "http://127.0.0.1:8080";
const client = createClient(GreetService, createConnectTransport({ baseUrl: base, httpVersion: "1.1" }));
const res = await client.greet({ name: "ts" });
console.log(`connect: ${res.greeting}`);

const variables: HelloQueryVariables = { name: "ts" };
const r = await fetch(base + "/graphql", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ query: "query Hello($name: String!) { hello(name: $name) { greeting length } }", variables }),
});
const body = (await r.json()) as { data: HelloQuery };
console.log(`graphql: ${body.data.hello.greeting} (${body.data.hello.length})`);
```

```go file=web/bob.go
//go:build bob

package web

import (
	greetv1 "example.com/helloapi/proto/greet/v1"
	"example.com/helloapi/schema"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/npm"
)

// Client is a Node program using generated connect-es and graphql-codegen code (label //web:client).
var Client = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{
		Name:  "client",
		Entry: "src/client.ts",
		Deps: []npm.Dependency{
			npm.Dep("@connectrpc/connect", "^2"),
			npm.Dep("@connectrpc/connect-node", "^2"),
			npm.Dep("@types/node", "^24", npm.Dev()),
		},
		Generated: []npm.Generated{
			connect.TS(greetv1.Greet(p), connect.TSOpts{}),
			graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")}),
		},
	})
})
```

## Lock and build

```bash run
bob lock
```

```text expect
bob.lock: … changes
  + go       connectrpc.com/connect                          …
…
  + nixpkgs  nixpkgs   …
…
  + npm      @connectrpc/connect   2.…
```

The Go section is long: `bob lock` pins the whole module graph that the generators' runtime
libraries require, not only the modules your code compiles.

```bash run
bob build
```

```text expect
built   //:gen-graph
built   //:gen-proto-greet-v1
built   //:gen-rest
built   //cmd/client:client
built   //cmd/server:server
built   //web:client
built   //web:gen-graphql
built   //web:gen-proto-greet-v1
…
bob: 8 built
```

Each generated tree is a package of its own (`//:gen-proto-greet-v1`, …). bob builds
generators first and stores their output by content: a change to a schema that produces the
same code (a comment, formatting) rebuilds nothing else.

## Generated code in your tree

bob writes the generated trees into the module, read-only, with a `.gitignore` that keeps them
out of git:

```bash run
ls gen gen/proto/greet/v1
cat gen/rest/.gitignore
```

```text expect
graph
proto
rest
gen/proto/greet/v1:
greet.pb.go
greetv1connect
# Generated by bob. Do not edit files in this directory: change the inputs and run `bob gen`.
*
```

`bob gen` writes them without building anything else, for example right after a clone so
that your editor resolves the imports. `bob status` shows whether each generated tree is
`fresh`, `stale`, `missing`, `edited` or an `orphan`.

## Run it

Start the server in the background, then call it with both clients and with curl:

```bash run
bob run //cmd/server:server > server.log 2>&1 &
sleep 5
bob run //cmd/client:client
bob run //web:client
curl -s http://127.0.0.1:8080/rest/items/7
```

```text expect
hello, bob
connect: hello, ts
graphql: hello, ts (9)
{"id":"7","name":"item 7"}
```

## Lint the protobuf API

`proto.Lint` is a check package: `bob test` runs `buf lint` on the library.
`proto.Breaking(p, lib, "origin/main")` adds a `buf breaking` check against a git revision.

```bash run
bob test
```

```text expect
PASS    //proto/greet/v1:lint (…s)
bob: 1 passed, 0 failed, 0 cached
```

## Next steps

- [Generated code guide](../04-guides/04-generated-code.md): ownership, drift and generator versions.
- Code generator options: [bob.go reference](../04-guides/01-bob-go-reference.md#code-generators).
- Ship the server: [Images, tarballs and AppImages](07-outputs.md).
