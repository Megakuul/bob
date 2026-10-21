// Package nixidx resolves nixpkgs attributes for a pinned revision into derivation paths,
// output paths, status flags and hashDerivationModulo values, cached on disk per revision.
package nixidx

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"bobspike/s04/drv"
)

//go:embed resolve.nix
var resolveNix []byte

type Attr struct {
	Missing              bool              `json:"missing,omitempty"`
	Broken               bool              `json:"broken,omitempty"`
	Insecure             bool              `json:"insecure,omitempty"`
	KnownVulnerabilities []string          `json:"knownVulnerabilities,omitempty"`
	Unfree               bool              `json:"unfree,omitempty"`
	Available            bool              `json:"available"`
	EvalRefused          bool              `json:"evalRefused,omitempty"`
	Version              string            `json:"version,omitempty"`
	DrvPath              string            `json:"drvPath,omitempty"`
	Outputs              map[string]string `json:"outputs,omitempty"`
}

type Index struct {
	Rev    string            `json:"rev"`
	System string            `json:"system"`
	Src    string            `json:"src"`
	Names  []string          `json:"names"`
	Attrs  map[string]*Attr  `json:"attrs"`
	Modulo map[string]string `json:"modulo"`

	path  string
	Cache *drv.ModuloCache `json:"-"`
	Stats Stats            `json:"-"`
}

type Stats struct {
	Fetched  bool
	Resolved []string
	Parsed   int
}

func cacheDir() string {
	d := os.Getenv("XDG_CACHE_HOME")
	if d == "" {
		d = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(d, "bob-s04")
}

// Open loads the cached index for rev or creates it (fetching the nixpkgs source and its names).
func Open(rev, system string) (*Index, error) {
	p := filepath.Join(cacheDir(), "index-"+rev[:12]+"-"+system+".json")
	ix := &Index{Rev: rev, System: system, Attrs: map[string]*Attr{}, Modulo: map[string]string{}, path: p, Cache: drv.NewModuloCache()}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, ix); err == nil {
			if _, err := os.Stat(ix.Src); err == nil {
				ix.path, ix.Cache = p, drv.NewModuloCache()
				ix.Cache.Import(ix.Modulo)
				return ix, nil
			}
		}
	}
	out, err := nix(nil, "flake", "prefetch", "--json", "github:NixOS/nixpkgs/"+rev)
	if err != nil {
		return nil, err
	}
	var pf struct{ StorePath string }
	if err := json.Unmarshal(out, &pf); err != nil {
		return nil, err
	}
	ix.Src = pf.StorePath
	ix.Stats.Fetched = true
	res, err := ix.eval(nil, true)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(res["__names"], &ix.Names); err != nil {
		return nil, err
	}
	sort.Strings(ix.Names)
	return ix, ix.Save()
}

func (ix *Index) Save() error {
	ix.Modulo = ix.Cache.Export()
	b, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ix.path), 0o755); err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ix.path)
}

func (ix *Index) eval(attrs []string, names bool) (map[string]json.RawMessage, error) {
	f := filepath.Join(cacheDir(), "resolve.nix")
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(f, resolveNix, 0o644); err != nil {
		return nil, err
	}
	if attrs == nil {
		attrs = []string{}
	}
	al, _ := json.Marshal(attrs)
	expr := fmt.Sprintf("import %s { src = %s; system = %q; attrs = builtins.fromJSON %q; names = %v; }", f, ix.Src, ix.System, string(al), names)
	out, err := nix(nil, "eval", "--json", "--impure", "--expr", expr)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	return m, json.Unmarshal(out, &m)
}

// Resolve makes sure attrs are in the index (one targeted evaluation for the missing ones).
func (ix *Index) Resolve(attrs []string) error {
	var need []string
	for _, a := range attrs {
		if _, ok := ix.Attrs[a]; !ok {
			need = append(need, a)
		}
	}
	if len(need) == 0 {
		return nil
	}
	res, err := ix.eval(need, false)
	if err != nil {
		return err
	}
	for _, a := range need {
		var at Attr
		if err := json.Unmarshal(res[a], &at); err != nil {
			return err
		}
		ix.Attrs[a] = &at
	}
	ix.Stats.Resolved = append(ix.Stats.Resolved, need...)
	return ix.Save()
}

// Suggest returns up to 3 known top-level names close to a missing one.
func (ix *Index) Suggest(name string) []string {
	type c struct {
		n string
		d int
	}
	var cs []c
	for _, n := range ix.Names {
		if d := lev(strings.ToLower(n), strings.ToLower(name)); d <= 2 {
			cs = append(cs, c{n, d})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d || cs[i].d == cs[j].d && cs[i].n < cs[j].n })
	var out []string
	for i := 0; i < len(cs) && i < 3; i++ {
		out = append(out, cs[i].n)
	}
	return out
}

func lev(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func nix(stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("nix", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, &NixError{Args: args, Stderr: stderr.String(), Err: err}
	}
	return out, nil
}

// NixError keeps raw Nix output for -v; callers show a bob message instead.
type NixError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *NixError) Error() string { return "nix " + strings.Join(e.Args, " ") + ": " + e.Err.Error() }
