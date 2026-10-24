package npm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type pkgJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	Workspaces           []string          `json:"workspaces"`
}

// InputFromDir reads package.json (+ workspaces) as the stand-in for bob.go declarations.
func InputFromDir(dir string) (*Input, error) {
	in := &Input{Importers: map[string]*Importer{}}
	root, err := readPkg(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	in.Importers["."] = root.importer()
	var bj struct {
		Extend map[string]map[string]string `json:"extend"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "bob.json")); err == nil {
		if err := json.Unmarshal(b, &bj); err != nil {
			return nil, fmt.Errorf("bob.json: %w", err)
		}
		in.Extend = bj.Extend
	}
	var wss []string
	for _, pat := range root.Workspaces {
		m, _ := filepath.Glob(filepath.Join(dir, pat, "package.json"))
		wss = append(wss, m...)
	}
	sort.Strings(wss)
	for _, f := range wss {
		p, err := readPkg(f)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(dir, filepath.Dir(f))
		in.Importers[filepath.ToSlash(rel)] = p.importer()
	}
	return in, nil
}

func readPkg(f string) (*pkgJSON, error) {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	var p pkgJSON
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", f, err)
	}
	return &p, nil
}

func (p *pkgJSON) importer() *Importer {
	deps := map[string]string{}
	for k, v := range p.Dependencies {
		if _, opt := p.OptionalDependencies[k]; !opt {
			deps[k] = v
		}
	}
	return &Importer{Name: p.Name, Version: p.Version, Deps: deps, DevDeps: p.DevDependencies,
		OptionalDeps: p.OptionalDependencies, PeerDeps: p.PeerDependencies}
}

// NpmLock is the subset of package-lock.json v3 used for comparison.
type NpmLock struct {
	Packages map[string]struct {
		Name                 string            `json:"name"`
		Version              string            `json:"version"`
		Link                 bool              `json:"link"`
		Resolved             string            `json:"resolved"`
		Integrity            string            `json:"integrity"`
		Dev                  bool              `json:"dev"`
		Optional             bool              `json:"optional"`
		Peer                 bool              `json:"peer"`
		InBundle             bool              `json:"inBundle"`
		Dependencies         map[string]string `json:"dependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OS                   []string          `json:"os"`
		CPU                  []string          `json:"cpu"`
		Libc                 []string          `json:"libc"`
	} `json:"packages"`
}

// realName returns the package name of an npm lock entry (aliases carry "name").
func realNameOf(path, name string) string {
	if name != "" {
		return name
	}
	i := strings.LastIndex(path, "node_modules/")
	return path[i+len("node_modules/"):]
}

type CompareResult struct {
	NpmPkgs, BobPkgs, Common int
	OnlyNpm, OnlyBob        []string
	EdgesChecked, EdgeDiffs int
	EdgeDiffList            []string
	BundledSkipped          int
}

// Compare compares the set of name@version (registry packages) and the resolved edges.
func Compare(nl *NpmLock, bl *Lock) *CompareResult {
	r := &CompareResult{}
	npmSet := map[string]bool{}
	for path, p := range nl.Packages {
		if path == "" || p.Link || !strings.Contains(path, "node_modules/") {
			continue
		}
		if p.InBundle {
			r.BundledSkipped++
			continue
		}
		npmSet[realNameOf(path, p.Name)+"@"+p.Version] = true
	}
	bobSet := map[string]bool{}
	for _, p := range bl.Packages {
		bobSet[p.Name+"@"+p.Version] = true
	}
	for k := range npmSet {
		if bobSet[k] {
			r.Common++
		} else {
			r.OnlyNpm = append(r.OnlyNpm, k)
		}
	}
	for k := range bobSet {
		if !npmSet[k] {
			r.OnlyBob = append(r.OnlyBob, k)
		}
	}
	sort.Strings(r.OnlyNpm)
	sort.Strings(r.OnlyBob)
	r.NpmPkgs, r.BobPkgs = len(npmSet), len(bobSet)
	// edges: for each npm package, resolve each dependency by walking up node_modules.
	find := func(from, dep string) string {
		base := from
		for {
			cand := dep
			if base != "" {
				cand = base + "/node_modules/" + dep
			} else {
				cand = "node_modules/" + dep
			}
			if p, ok := nl.Packages[cand]; ok {
				return realNameOf(cand, p.Name) + "@" + p.Version
			}
			if base == "" {
				return ""
			}
			if i := strings.LastIndex(base, "/node_modules/"); i >= 0 {
				base = base[:i]
			} else {
				base = ""
			}
		}
	}
	bobEdges := map[string]map[string]string{}
	for _, p := range bl.Packages {
		k := p.Name + "@" + p.Version
		if bobEdges[k] == nil {
			bobEdges[k] = map[string]string{}
		}
		for a, d := range p.Deps {
			if lp := bl.Packages[d]; lp != nil {
				bobEdges[k][a] = lp.Name + "@" + lp.Version
			}
		}
	}
	seen := map[string]bool{}
	for path, p := range nl.Packages {
		if path == "" || p.Link || p.InBundle || !strings.Contains(path, "node_modules/") {
			continue
		}
		k := realNameOf(path, p.Name) + "@" + p.Version
		be := bobEdges[k]
		if be == nil {
			continue
		}
		for dep := range p.Dependencies {
			got := find(path, dep)
			if tp, ok := nl.Packages[path+"/node_modules/"+dep]; ok && tp.InBundle {
				continue
			}
			if got == "" {
				continue
			}
			ek := k + " -> " + dep
			if seen[ek+got] {
				continue
			}
			seen[ek+got] = true
			r.EdgesChecked++
			if be[dep] != got {
				r.EdgeDiffs++
				r.EdgeDiffList = append(r.EdgeDiffList, fmt.Sprintf("%s: npm %s, bob %s", ek, got, be[dep]))
			}
		}
	}
	sort.Strings(r.EdgeDiffList)
	return r
}
