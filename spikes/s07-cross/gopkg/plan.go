// Package gopkg plans Go builds at package granularity: it reads Go sources in-process with
// go/build (no `go` command, no network) and produces a package graph that the drv layer
// turns into one derivation per package, per module, or per group.
package gopkg

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Module is one Go module taking part in the build.
type Module struct {
	Path    string // module path
	Version string // "" for workspace modules
	Dir     string // directory that is parsed (unpacked store path, or the working tree)
	Lang    string // `go` directive, e.g. "1.23" (drives -lang)
	Main    bool   // workspace module
}

// Trim is the -trimpath replacement prefix Go uses for this module (mirrors cmd/go).
func (m *Module) Trim() string {
	if m.Version == "" {
		return m.Path
	}
	return m.Path + "@" + m.Version
}

// Pkg is one Go package (non-std).
type Pkg struct {
	ImportPath string
	Name       string
	Mod        *Module
	Dir        string // directory parsed by the planner
	Rel        string // directory relative to the module root ("" for root)
	GoFiles    []string
	CgoFiles   []string
	CFiles     []string
	HFiles     []string
	SFiles     []string
	CgoCFLAGS  []string
	CgoLDFLAGS []string
	Embed      map[string][]string // pattern -> files relative to Dir
	Imports    []string            // non-std imports
	StdImports []string
	Generated  bool // comes from a codegen derivation, not the working tree
	Test       bool
}

// Files returns every file of the package that must be in its source store path.
func (p *Pkg) Files() []string {
	var fs []string
	fs = append(fs, p.GoFiles...)
	fs = append(fs, p.CgoFiles...)
	fs = append(fs, p.CFiles...)
	fs = append(fs, p.HFiles...)
	fs = append(fs, p.SFiles...)
	for _, e := range p.Embed {
		fs = append(fs, e...)
	}
	sort.Strings(fs)
	return uniq(fs)
}

// Plan is the resolved package graph of one Go binary.
type Plan struct {
	Pkgs    map[string]*Pkg
	Order   []*Pkg // dependencies first
	Main    *Pkg
	Modules []*Module
}

// Planner resolves imports against a fixed module set. Std is anything whose first path element
// has no dot (Go's own rule for module mode).
type Planner struct {
	Ctx       build.Context
	Modules   []*Module         // workspace + required modules
	Overrides map[string]string // import path -> dir (generated packages)
}

func NewPlanner(goroot, goarch string, cgo bool, toolTags []string) *Planner {
	c := build.Default
	c.GOROOT = goroot
	c.GOOS, c.GOARCH = "linux", goarch
	c.ToolTags = append(append([]string{}, c.ToolTags...), toolTags...)
	c.CgoEnabled = cgo
	c.Compiler = "gc"
	c.GOPATH = ""
	return &Planner{Ctx: c, Overrides: map[string]string{}}
}

func IsStd(ip string) bool {
	first, _, _ := strings.Cut(ip, "/")
	return !strings.Contains(first, ".")
}

func (pl *Planner) resolve(ip string) (*Module, string, error) {
	var best *Module
	for _, m := range pl.Modules {
		if (ip == m.Path || strings.HasPrefix(ip, m.Path+"/")) && (best == nil || len(m.Path) > len(best.Path)) {
			best = m
		}
	}
	if best == nil {
		return nil, "", fmt.Errorf("no module provides package %s", ip)
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(ip, best.Path), "/"), nil
}

// Load plans the closure of the main package mainIP.
func (pl *Planner) Load(mainIP string) (*Plan, error) {
	pk := &Plan{Pkgs: map[string]*Pkg{}, Modules: pl.Modules}
	state := map[string]int{}
	var visit func(ip string, from string) error
	visit = func(ip, from string) error {
		switch state[ip] {
		case 1:
			return fmt.Errorf("import cycle through %s (imported by %s)", ip, from)
		case 2:
			return nil
		}
		state[ip] = 1
		p, err := pl.loadOne(ip)
		if err != nil {
			if from != "" {
				return fmt.Errorf("%s (imported by %s): %w", ip, from, err)
			}
			return err
		}
		for _, d := range p.Imports {
			if err := visit(d, ip); err != nil {
				return err
			}
		}
		state[ip] = 2
		pk.Pkgs[ip] = p
		pk.Order = append(pk.Order, p)
		return nil
	}
	if err := visit(mainIP, ""); err != nil {
		return nil, err
	}
	pk.Main = pk.Pkgs[mainIP]
	return pk, nil
}

func (pl *Planner) loadOne(ip string) (*Pkg, error) {
	m, rel, err := pl.resolve(ip)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.Dir, rel)
	gen := false
	if d, ok := pl.Overrides[ip]; ok {
		dir, gen = d, true
	}
	bp, err := pl.Ctx.ImportDir(dir, 0)
	if err != nil {
		return nil, err
	}
	p := &Pkg{ImportPath: ip, Name: bp.Name, Mod: m, Dir: dir, Rel: rel, Generated: gen,
		GoFiles: bp.GoFiles, CgoFiles: bp.CgoFiles, CFiles: bp.CFiles, HFiles: bp.HFiles, SFiles: bp.SFiles,
		CgoCFLAGS: bp.CgoCFLAGS, CgoLDFLAGS: bp.CgoLDFLAGS}
	if len(bp.CgoPkgConfig) > 0 {
		return nil, fmt.Errorf("#cgo pkg-config is not supported by the spike")
	}
	for _, i := range bp.Imports {
		if i == "C" {
			continue
		}
		if IsStd(i) {
			p.StdImports = append(p.StdImports, i)
		} else {
			p.Imports = append(p.Imports, i)
		}
	}
	if len(bp.EmbedPatterns) > 0 {
		p.Embed = map[string][]string{}
		for _, pat := range bp.EmbedPatterns {
			fs, err := expandEmbed(dir, pat)
			if err != nil {
				return nil, fmt.Errorf("go:embed %s: %w", pat, err)
			}
			p.Embed[pat] = fs
		}
	}
	return p, nil
}

// expandEmbed implements the documented //go:embed pattern rules (glob; directories recursive,
// skipping . and _ files unless the pattern starts with all:).
func expandEmbed(dir, pat string) ([]string, error) {
	all := strings.HasPrefix(pat, "all:")
	pat = strings.TrimPrefix(pat, "all:")
	ms, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
	if err != nil || len(ms) == 0 {
		return nil, fmt.Errorf("no matching files")
	}
	var out []string
	for _, m := range ms {
		fi, err := os.Stat(m)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			r, _ := filepath.Rel(dir, m)
			out = append(out, filepath.ToSlash(r))
			continue
		}
		err = filepath.Walk(m, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			b := fi.Name()
			if p != m && !all && (strings.HasPrefix(b, ".") || strings.HasPrefix(b, "_")) {
				if fi.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !fi.IsDir() {
				r, _ := filepath.Rel(dir, p)
				out = append(out, filepath.ToSlash(r))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GoDirective reads the `go` line of a go.mod (default 1.16 as cmd/go does for missing lines).
func GoDirective(gomod string) string {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return "1.16"
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "go" {
			v := f[1]
			if parts := strings.Split(v, "."); len(parts) > 2 { // go 1.23.4 -> 1.23
				v = parts[0] + "." + parts[1]
			}
			return v
		}
	}
	return "1.16"
}

func uniq(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}
