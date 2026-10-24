---
title: The build graph
section: Concepts
order: 1
summary: How bob.go files become a graph of small, cacheable packages.
placeholder: true
---

Every `bob.go` file declares packages with `bob.Define`. bob evaluates them into a graph
and builds each node in a sandbox.

```go
// cmd/api/bob.go: the binary of this directory's main package, bound to the slot acme.API.
var API = bob.Define(func(p *bob.P) *golang.Binary {
	return acme.Module(p).Binary(golang.BinaryOpts{})
})

var _ = acme.API.Bind(API)

// deploy/bob.go: the image takes the binary's closure, one layer per package.
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "api", oci.ImageOpts{Program: acme.API.Get(p), Ports: []int{8080}})
})
```

## Early cutoff

When a package rebuilds but its output is bit-identical, its dependents are **not** rebuilt.

1. bob hashes the new output.
2. If the hash matches the cached one, the dependents stay cache hits.
   - nested detail
3. Otherwise they rebuild.

## Interfaces

```proto
service BuildService {
  rpc Start(StartRequest) returns (stream Event);
}
```

```json
{ "label": "//cmd/api:api", "cached": true, "ms": 12 }
```

```ts
const res = await client.start({ label: '//cmd/api:api' });
```
