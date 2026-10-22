package engine

import (
	"context"
	"encoding/json"
	"io/fs"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/nixidx"
)

// Ecosystem is implemented by every ecosystem's generator package (SPEC R-FW-051, §03.0.1) and
// registered from its init with Register. It is never linked into the evaluator.
type Ecosystem interface {
	Name() string // bob.lock section key; also the prefix of the ecosystem's node kinds
	Lock(ctx context.Context, req LockRequest) (json.RawMessage, error)
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
	Native(ctx context.Context, req PlanRequest) ([]GeneratedFile, error)
}

// LockRequest asks an ecosystem to resolve its declarations (bob lock).
type LockRequest struct {
	Graph   *ir.Graph       // validated IR (all configurations)
	Prev    json.RawMessage // previous section, nil if none
	Update  []string        // names to move (bob lock --update), empty = keep pins
	Fetch   fetch.Fetcher
	Nixpkgs *nixidx.Index
}

// PlanRequest asks an ecosystem for the derivations of its nodes. Plan must be deterministic
// and run no processes.
type PlanRequest struct {
	Graph   *ir.Graph
	Nodes   []*ir.Node          // this ecosystem's nodes (by kind prefix "<name>.")
	Lock    json.RawMessage     // this ecosystem's section
	Inputs  map[string]Resolved // node id → resolved deps: drv paths/outputs, store paths, LinkInfo
	Sources map[string]string   // node id → imported source store path (R-NIX-009)
	Nixpkgs *nixidx.Index
	Builder string // bob-builder store path for the host system
}

// PlanResult is what Plan returns.
type PlanResult struct {
	Drvs    []*drv.Derivation   // new derivations, any order (bob sorts topologically)
	Nodes   map[string]Resolved // node id → its outputs and, for Linkables, filled LinkInfo
	Fetches []fetch.Request     // external inputs; the fetcher makes their paths valid before nix build
}

// Resolved is a node's outputs after planning.
type Resolved struct {
	Drv         string            // drv path ("" for plain store paths)
	Outputs     map[string]string // output name → store path
	LinkInfo    *bob.LinkInfo
	MainProgram string
}

// GeneratedFile is one native tooling file an ecosystem writes (§03.0.2).
type GeneratedFile struct {
	Path    string // repo-relative
	Content []byte
	Mode    fs.FileMode
	Owner   string // label of the owning package
}

// Register registers an ecosystem. Generator packages call it from init.
func Register(e Ecosystem) {}

// Substituter is a binary cache bob passes to Nix (URL, public key, optional netrc credential).
type Substituter struct{ URL, PublicKey, NetrcMachine, NetrcPassword string }

// RemoteExecutor runs a build elsewhere and makes the outputs available locally (bob build --remote).
type RemoteExecutor interface {
	Build(ctx context.Context, req RemoteRequest) (<-chan Event, error)
}

// RemoteRequest is one remote build (SPEC §02.6).
type RemoteRequest struct {
	Root      string
	Patterns  []string
	Platforms []bob.Platform
	Variants  []string
}

// Event is one --json event of a build (SPEC §02.4).
type Event struct {
	Name string          // "package.finish", …
	Data json.RawMessage // the event object
}
