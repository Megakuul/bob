package ir

import (
	"encoding/json"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Limits of a valid IR document (R-FW-047). Larger input is rejected before use.
const (
	MaxBytes  = 64 << 20  // whole document
	MaxNodes  = 1_000_000 // nodes[]
	MaxString = 64 << 10  // any string
)

// Graph is one IR v1 document as the evaluator writes it to fd 3 (SPEC §1.6).
type Graph struct {
	IRVersion  int               `json:"irVersion"`
	BobVersion string            `json:"bobVersion"`
	Module     string            `json:"module"`
	Repo       Repo              `json:"repo"`
	Configs    []Config          `json:"configs"`
	Nodes      []*Node           `json:"nodes"`
	Roots      []string          `json:"roots"`
	Pipelines  []json.RawMessage `json:"pipelines"`
}

// Repo is RepoOpts as the evaluator saw it.
type Repo struct {
	Nixpkgs  string            `json:"nixpkgs"`
	Variants []json.RawMessage `json:"variants"`
}

// Config is one requested configuration.
type Config struct {
	ID         string              `json:"id"`
	Platform   string              `json:"platform"`
	Profile    string              `json:"profile"`
	Sanitizers []string            `json:"sanitizers"`
	Race       bool                `json:"race"`
	Features   map[string][]string `json:"features"`
	Named      []string            `json:"named"`
}

// Node is one (package, projected config).
type Node struct {
	ID        string                     `json:"id"`
	Kind      string                     `json:"kind"`
	Label     string                     `json:"label"`
	DefinedAt Pos                        `json:"definedAt"`
	Proj      map[string]json.RawMessage `json:"proj"`
	Deps      []string                   `json:"deps"`
	Srcs      []Srcs                     `json:"srcs"`
	Spec      json.RawMessage            `json:"spec"`
	Nix       *Nix                       `json:"nix,omitempty"`
}

// Pos is a module-relative bob.go position.
type Pos struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Srcs is one Sources value, resolved by bob (not by the evaluator).
type Srcs struct {
	Dir      string   `json:"dir"`
	Patterns []string `json:"patterns"`
}

// Nix describes a nixpkgs: node.
type Nix struct {
	Attr    string   `json:"attr"`
	Set     string   `json:"set"` // host | target | cross
	Outputs []string `json:"outputs"`
	Allow   Allow    `json:"allow"`
}

// Allow lists accepted knownVulnerabilities entries and the unfree permission.
type Allow struct {
	Insecure []string `json:"insecure"`
	Unfree   bool     `json:"unfree"`
}

// Parse validates and decodes an IR document produced by the evaluator of bob version
// bobVersion. IR is hostile input: size, node count, string length, dependency ids, cycles
// (BOB-E-1006) and kinds are checked; another bobVersion is an internal error (R-FW-044).
func Parse(data []byte, bobVersion string) (*Graph, error) {
	return nil, diag.NotImplemented("ir.Parse")
}
