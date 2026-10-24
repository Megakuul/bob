---
title: Pipelines
section: Guides
order: 6
summary: Declare CI pipelines in bob.go, run them locally with bob pipeline run, and let a bob server run them on pushes, pull requests and tags.
---

A pipeline is a named list of what to build, what to test and which releases to publish,
declared in `bob.go`. In this guide you declare one, run it on your machine, and see how a
bob server runs the same pipeline from forge events.

## Declare a pipeline

Pipelines live in the root `bob.go` (or any `bob.go`), next to the code:

```go file=bob.go
//go:build bob

package hello

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/hello", Go: "1.26"})
})

var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests("./...", golang.TestOpts{})
})

// Pipeline ci builds and tests everything on pushes to main and on pull requests.
var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnPush("main"), bob.OnPullRequest()},
	Targets:  []bob.Pattern{"//..."},
	Tests:    []bob.Pattern{"//..."},
})

// Pipeline release publishes the release "hello" when a tag v* is pushed.
var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers:    []bob.Trigger{bob.OnTag("v*")},
	Concurrency: bob.Queue,
	Releases:    []string{"hello"},
})
```

```go file=greet/greet.go
package greet

// Hello returns a greeting for name.
func Hello(name string) string { return "hello, " + name }
```

```go file=greet/greet_test.go
package greet

import "testing"

func TestHello(t *testing.T) {
	if Hello("bob") != "hello, bob" {
		t.Fatal("unexpected greeting")
	}
}
```

| Field | Meaning |
|---|---|
| `Targets` | patterns to build (the `build` step) |
| `Tests` | patterns to test (the `test` step) |
| `Releases` | names of `release.New` packages to publish; makes it a release pipeline |
| `Triggers` | `bob.OnPush(branchGlob)`, `bob.OnPullRequest()`, `bob.OnTag(glob)`; none means manual runs only |
| `Concurrency` | what happens to a running build of the same ref: `bob.Queue` (wait), `bob.Supersede` (cancel the older one), `bob.Parallel`; unset means queue for the default branch and tags, supersede for other branches and pull requests |

## Run it locally

`bob pipeline run` runs the steps on your machine, with the same output as `bob build` and
`bob test`. The exit status is the first failing step's:

```bash run
bob lock
bob pipeline run ci
```

```text expect
…
== step build
…
== step test
PASS    //greet:go_test (…s)
bob: 1 passed, 0 failed, 0 cached
```

This is exactly what a CI system needs: `bob pipeline run ci` in any CI runner that has bob
and user namespaces. An unknown pipeline name exits 2 with `BOB-E-0001`.

Locally, only the build and test steps run. Publishing releases is the server's job: a
pipeline that only lists `Releases` (like `release` above) does nothing on your machine. To
build a release locally, build its package with a version, as in
[Releases](07-releases.md#build-a-release-locally). The release `hello` named here must exist
as a `release.New` package for the server to publish it.

## Run it on a bob server

A bob server runs pipelines on its worker fleet:

- from forge events: when a project is connected to a GitHub, GitLab or Forgejo repository,
  pushes, pull requests and tags start the pipelines whose triggers match, and the server
  reports commit statuses back (see [Projects and forges](../06-server/06-projects-and-forges.md));
- from the web UI and the API (manual runs, retries, re-runs, cancel).

`bob pipeline run <name> --remote` (submit from the CLI) is not implemented yet; it exits
with `BOB-E-9008`. `bob build --remote` and `bob test --remote` submit ad-hoc builds; see
[CI and remote builds](../06-server/07-ci-and-remote-builds.md).

## Next steps

- [Releases](07-releases.md).
- [CI and remote builds](../06-server/07-ci-and-remote-builds.md).
- [CLI reference: bob pipeline](../05-cli/01-commands.md#bob-pipeline).
