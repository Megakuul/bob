package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/bob/ext"
)

// Limits of a valid IR document (R-FW-047). Larger input is rejected before use.
const (
	MaxBytes  = 64 << 20  // whole document
	MaxNodes  = 1_000_000 // nodes[]
	MaxString = 64 << 10  // any string
	maxDepth  = 128       // JSON nesting
)

// Version is the IR version this package reads.
const Version = 1

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

// Pos is a module-relative bob.go position. Col is the 1-based byte column of the constructor
// call (0 when unknown); error messages show file:line, bob graph file:line:col.
type Pos struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
}

// String returns "libs/foo/bob.go:12" (without the column: the form of error messages).
func (p Pos) String() string { return fmt.Sprintf("%s:%d", p.File, p.Line) }

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
	Modules []string `json:"modules,omitempty"` // pkg-config modules a nixpkgs.Lib links (Modules option)
	Allow   Allow    `json:"allow"`
}

// Allow lists accepted knownVulnerabilities entries and the unfree permission.
type Allow struct {
	Insecure []string `json:"insecure"`
	Unfree   bool     `json:"unfree"`
}

// Ecosystems whose kinds ("<name>.<kind>") this binary can plan; engine.Register adds them. The
// set is written by init functions only.
var (
	ecoMu      sync.Mutex
	ecosystems = map[string]bool{}
)

// RegisterEcosystem makes the kinds "<name>.*" known to Parse. engine.Register calls it.
func RegisterEcosystem(name string) {
	ecoMu.Lock()
	defer ecoMu.Unlock()
	ecosystems[name] = true
}

// KnownKind reports whether kind is a kind this binary handles: a kind registered with
// ext.Register in a linked package (including bob's core kinds), or a kind of a registered
// ecosystem.
func KnownKind(kind string) bool {
	if _, ok := ext.Lookup(kind); ok {
		return true
	}
	eco, _, _ := strings.Cut(kind, ".")
	ecoMu.Lock()
	defer ecoMu.Unlock()
	return ecosystems[eco]
}

var kindName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)*$`) })

// invalid is the structured error for IR that breaks the format (internal class: the evaluator
// bob generated wrote it).
func invalid(format string, args ...any) error {
	return diag.New(9004, "label", "the evaluator output", "reason", fmt.Sprintf(format, args...))
}

// Parse validates and decodes an IR document produced by the evaluator of bob version
// bobVersion. IR is hostile input: size, node count, string length, dependency ids, cycles
// (BOB-E-1006) and kinds are checked; another bobVersion is an internal error (R-FW-044).
//
// Dependencies and roots name nodes by id (A67).
func Parse(data []byte, bobVersion string) (*Graph, error) {
	if len(data) > MaxBytes {
		return nil, invalid("the graph is %d bytes, more than %d", len(data), MaxBytes)
	}
	if err := scan(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var g Graph
	if err := dec.Decode(&g); err != nil {
		return nil, invalid("not a graph document: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("data after the graph document")
	}
	if g.IRVersion != Version {
		return nil, invalid("irVersion %d, want %d", g.IRVersion, Version)
	}
	if g.BobVersion != bobVersion {
		return nil, invalid("the evaluator is bob %s, this is bob %s", g.BobVersion, bobVersion)
	}
	if err := g.validate(); err != nil {
		return nil, err
	}
	return &g, nil
}

// scan checks the bounds that decoding would otherwise have to allocate for: nesting depth,
// string length and the number of nodes. It streams tokens and keeps nothing.
func scan(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []*level
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return invalid("not JSON: %v", err)
		}
		var top *level
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if s, ok := tok.(string); ok && len(s) > MaxString {
			return invalid("a string is %d bytes, more than %d", len(s), MaxString)
		}
		if top != nil && top.object && top.wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				valueDone(stack)
				continue
			}
			top.key, _ = tok.(string)
			top.wantKey = false
			continue
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			stack = stack[:len(stack)-1]
			valueDone(stack)
			continue
		}
		// tok starts a value.
		if top != nil && top.nodes {
			if top.count++; top.count > MaxNodes {
				return invalid("more than %d nodes", MaxNodes)
			}
		}
		if d, ok := tok.(json.Delim); ok {
			if len(stack) >= maxDepth {
				return invalid("nested deeper than %d levels", maxDepth)
			}
			nodes := d == '[' && len(stack) == 1 && top.object && top.key == "nodes"
			stack = append(stack, &level{object: d == '{', wantKey: d == '{', nodes: nodes})
			continue
		}
		valueDone(stack)
	}
}

// level is one open JSON container during scan.
type level struct {
	object  bool
	wantKey bool
	key     string // last key, for objects
	nodes   bool   // the top-level "nodes" array
	count   int
}

// valueDone marks that the innermost object (if any) expects a key next.
func valueDone(stack []*level) {
	if len(stack) > 0 && stack[len(stack)-1].object {
		stack[len(stack)-1].wantKey = true
	}
}

// validate checks ids, kinds, dependencies, roots and cycles.
func (g *Graph) validate() error {
	byID := make(map[string]*Node, len(g.Nodes))
	for i, n := range g.Nodes {
		if n == nil || n.ID == "" {
			return invalid("node %d has no id", i)
		}
		if _, dup := byID[n.ID]; dup {
			return invalid("node id %s appears twice", n.ID)
		}
		if !kindName().MatchString(n.Kind) || !KnownKind(n.Kind) {
			return invalid("node %s has the unknown kind %q", n.ID, n.Kind)
		}
		byID[n.ID] = n
	}
	for _, n := range g.Nodes {
		for _, d := range n.Deps {
			if _, ok := byID[d]; !ok {
				return diag.New(1007, "label", name(n), "dep", d)
			}
		}
	}
	for _, r := range g.Roots {
		if _, ok := byID[r]; !ok {
			return invalid("root %s is not a node", r)
		}
	}
	return g.acyclic(byID)
}

func name(n *Node) string {
	if n.Label != "" {
		return n.Label
	}
	return n.ID
}

// acyclic reports the first dependency cycle as BOB-E-1006 with each node's definedAt. The walk
// is iterative: a hostile graph may be a chain of a million nodes.
func (g *Graph) acyclic(byID map[string]*Node) error {
	const (
		unseen = iota
		active
		done
	)
	state := make(map[*Node]int, len(g.Nodes))
	type step struct {
		n    *Node
		next int // index of the next dependency to visit
	}
	for _, root := range g.Nodes {
		if state[root] != unseen {
			continue
		}
		state[root] = active
		path := []step{{n: root}}
		for len(path) > 0 {
			top := &path[len(path)-1]
			if top.next == len(top.n.Deps) {
				state[top.n] = done
				path = path[:len(path)-1]
				continue
			}
			dep := byID[top.n.Deps[top.next]]
			top.next++
			switch state[dep] {
			case done:
			case active:
				start := 0
				for i, s := range path {
					if s.n == dep {
						start = i
					}
				}
				var parts []string
				for _, s := range path[start:] {
					parts = append(parts, fmt.Sprintf("%s (%s)", name(s.n), s.n.DefinedAt))
				}
				parts = append(parts, fmt.Sprintf("%s (%s)", name(dep), dep.DefinedAt))
				return diag.New(1006, "cycle", strings.Join(parts, " → "))
			default:
				state[dep] = active
				path = append(path, step{n: dep})
			}
		}
	}
	return nil
}

// Canonical returns the canonical bytes of g (SPEC §1.6): UTF-8 JSON with object keys sorted, no
// insignificant whitespace and no HTML escaping; nodes keep their order (the evaluator sorts them
// by id).
func (g *Graph) Canonical() ([]byte, error) {
	b, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
