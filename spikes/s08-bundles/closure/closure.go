// Package closure reads the runtime closure of store paths (spike: via `nix path-info`).
package closure

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
)

type Path struct {
	Path       string   `json:"-"`
	NarSize    int64    `json:"narSize"`
	References []string `json:"references"`
}

// Closure is the set of paths reachable from Roots, sorted by path.
type Closure struct {
	Roots []string
	Paths map[string]*Path
}

func Query(ctx context.Context, roots ...string) (*Closure, error) {
	args := append([]string{"path-info", "--json", "--json-format", "1", "-r"}, roots...)
	out, err := exec.CommandContext(ctx, "nix", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("query closure: %w", err)
	}
	m := map[string]*Path{}
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse closure: %w", err)
	}
	for k, v := range m {
		v.Path = k
	}
	return &Closure{Roots: roots, Paths: m}, nil
}

func (c *Closure) Sorted() []string {
	s := make([]string, 0, len(c.Paths))
	for k := range c.Paths {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

func (c *Closure) Size() (n int64) {
	for _, p := range c.Paths {
		n += p.NarSize
	}
	return
}

// Popularity: number of distinct paths in the closure that (transitively) depend on p,
// counting p itself. Widely shared libraries (glibc) score highest.
func (c *Closure) Popularity() map[string]int {
	pop := map[string]int{}
	for _, start := range c.Sorted() {
		seen := map[string]bool{}
		var walk func(string)
		walk = func(x string) {
			if seen[x] {
				return
			}
			seen[x] = true
			if p := c.Paths[x]; p != nil {
				for _, r := range p.References {
					walk(r)
				}
			}
		}
		walk(start)
		for x := range seen {
			pop[x]++
		}
	}
	return pop
}
