package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// Dependency bob repositories (R-FW-043.3, R-FW-022): Go modules required by the repo's go.mod
// whose bob.go packages a bob.go imports. Their go.mod and bob.go files are copied to
// .bob/eval/mods/<escaped module path>@<version>/ and swapped in with replace lines.

// goMod is the part of a go.mod the evaluator generation reads.
type goMod struct {
	module  string
	goDir   string
	require map[string]string // module → version
	replace map[string]string // module → local directory (relative to the go.mod's directory or absolute)
}

// parseGoMod reads module, go, require and directory replace lines (single and block form).
func parseGoMod(b []byte) goMod {
	m := goMod{require: map[string]string{}, replace: map[string]string{}}
	block := ""
	for raw := range strings.SplitSeq(string(b), "\n") {
		line := raw
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if block != "" {
			if f[0] == ")" {
				block = ""
				continue
			}
			f = append([]string{block}, f...)
		} else if len(f) == 2 && f[1] == "(" {
			block = f[0]
			continue
		}
		for i := range f {
			if u, err := strconv.Unquote(f[i]); err == nil {
				f[i] = u
			}
		}
		switch {
		case f[0] == "module" && len(f) >= 2:
			m.module = f[1]
		case f[0] == "go" && len(f) >= 2:
			m.goDir = f[1]
		case f[0] == "require" && len(f) >= 3:
			m.require[f[1]] = f[2]
		case f[0] == "replace":
			// replace a [v] => dir: only directory targets (no version after the target).
			arrow := -1
			for i, x := range f {
				if x == "=>" {
					arrow = i
				}
			}
			if arrow >= 2 && arrow == len(f)-2 {
				t := f[arrow+1]
				if strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") || filepath.IsAbs(t) {
					m.replace[f[1]] = t
				}
			}
		}
	}
	return m
}

// depModule is one dependency bob repository.
type depModule struct {
	path, version string
	src           string              // host directory of its source
	gomod         goMod               // its go.mod
	bob           map[string]*bobFile // module-relative dir → bob.go
}

// copyDir is the module's directory below .bob/eval/mods.
func (m *depModule) copyDir() string { return escapePath(m.path) + "@" + m.version }

// depSet is every dependency bob repository the evaluator needs.
type depSet struct {
	mods map[string]*depModule // module path → module
}

// lookup finds the module and directory of an import path of a dependency.
func (s *depSet) lookup(p string) (*depModule, string, bool) {
	best := ""
	for mp := range s.mods {
		if (p == mp || strings.HasPrefix(p, mp+"/")) && len(mp) > len(best) {
			best = mp
		}
	}
	if best == "" {
		return nil, "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p, best), "/")
	if rel == "" {
		rel = "."
	}
	return s.mods[best], rel, true
}

// sorted returns the modules by path.
func (s *depSet) sorted() []*depModule {
	out := make([]*depModule, 0, len(s.mods))
	for _, m := range s.mods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// resolveDeps loads the dependency bob repositories the bob.go files import, transitively.
func resolveDeps(ctx context.Context, tc toolchain, root string, rootMod goMod, own map[string]*bobFile, bobVersion string) (*depSet, error) {
	s := &depSet{mods: map[string]*depModule{}}
	var queue []string
	for _, f := range own {
		for _, imp := range f.imports {
			queue = append(queue, imp.path)
		}
	}
	var modcache string
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if isStd(p) || p == frameworkModule || strings.HasPrefix(p, frameworkModule+"/") ||
			p == rootMod.module || strings.HasPrefix(p, rootMod.module+"/") {
			continue
		}
		if _, _, ok := s.lookup(p); ok {
			continue
		}
		mp, version := requiredModule(p, rootMod, s)
		if mp == "" {
			continue // not required by any go.mod: R7 reports it
		}
		var src string
		if dir, ok := rootMod.replace[mp]; ok {
			src = dir
			if !filepath.IsAbs(src) {
				src = filepath.Join(root, src)
			}
		} else {
			if modcache == "" {
				var err error
				if modcache, err = hostModCache(ctx, tc); err != nil {
					return nil, err
				}
			}
			var err error
			if src, err = moduleSource(ctx, tc, modcache, mp, version); err != nil {
				return nil, err
			}
		}
		m, err := loadDep(mp, version, src)
		if err != nil {
			return nil, err
		}
		if need := m.gomod.require[frameworkModule]; need != "" && bobVersion != devVersion && semverLess(bobVersion, need) {
			return nil, ir.Errors{ir.FrameworkTooOld(mp, need, bobVersion)}
		}
		s.mods[mp] = m
		for _, f := range m.bob {
			for _, imp := range f.imports {
				queue = append(queue, imp.path)
			}
		}
	}
	return s, nil
}

// requiredModule finds the module providing import path p: the longest module path required
// by the root go.mod, else by an already loaded dependency.
func requiredModule(p string, rootMod goMod, s *depSet) (string, string) {
	best, version := "", ""
	consider := func(req map[string]string) {
		for mp, v := range req {
			if (p == mp || strings.HasPrefix(p, mp+"/")) && len(mp) > len(best) {
				best, version = mp, v
			}
		}
	}
	consider(rootMod.require)
	if best == "" {
		for _, m := range s.sorted() {
			consider(m.gomod.require)
		}
	}
	return best, version
}

// loadDep reads the go.mod and bob.go files of a dependency module at src.
func loadDep(mp, version, src string) (*depModule, error) {
	gm, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("bob repository %s@%s: %w", mp, version, err)
	}
	m := &depModule{path: mp, version: version, src: src, gomod: parseGoMod(gm), bob: map[string]*bobFile{}}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != src && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			if p != src {
				if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "bob.go" || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, filepath.Dir(p))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m.bob[rel] = parseBob(rel, mp+"@"+version+"/"+bobPath(rel), b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bob repository %s@%s: %w", mp, version, err)
	}
	return m, nil
}

// hostModCache is the user's Go module cache (where go mod download puts modules).
func hostModCache(ctx context.Context, tc toolchain) (string, error) {
	cmd := exec.CommandContext(ctx, tc.goroot+"/bin/go", "env", "GOMODCACHE")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("finding the Go module cache: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// moduleSource returns the extracted module mp@version, downloading it with the user's Go
// settings (GOPROXY, GOPRIVATE, …) when the module cache does not have it.
func moduleSource(ctx context.Context, tc toolchain, modcache, mp, version string) (string, error) {
	dir := filepath.Join(modcache, escapePath(mp)+"@"+version)
	if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && fi.Mode().IsRegular() {
		return dir, nil
	}
	cmd := exec.CommandContext(ctx, tc.goroot+"/bin/go", "mod", "download", "-json", mp+"@"+version)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=", "GO111MODULE=on")
	cmd.Dir = os.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var res struct{ Dir, Error string }
	_ = json.Unmarshal(out, &res)
	if res.Error != "" || runErr != nil || res.Dir == "" {
		reason := res.Error
		if reason == "" {
			reason = strings.TrimSpace(stderr.String())
		}
		return "", fmt.Errorf("downloading bob repository %s@%s: %s", mp, version, reason)
	}
	return res.Dir, nil
}

// escapePath is the module cache's case encoding of a module path (golang.org/x/mod/module).
func escapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// semverLess compares two semantic versions (vMAJOR.MINOR.PATCH[-pre]); a pre-release is lower
// than its release.
func semverLess(a, b string) bool {
	pa, prea, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	pb, preb, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	if versionLess(pa, pb) {
		return true
	}
	if versionLess(pb, pa) {
		return false
	}
	switch {
	case prea == preb:
		return false
	case prea == "":
		return false
	case preb == "":
		return true
	}
	return prea < preb
}

// depGoMod is the go.mod of a dependency copy: module and go lines only. The evaluator's own
// go.mod lists every module the build needs.
func depGoMod(m *depModule) []byte {
	s := "module " + m.path + "\n"
	if m.gomod.goDir != "" {
		s += "\ngo " + m.gomod.goDir + "\n"
	}
	return []byte(s)
}

// relImport is the import path of a module-relative directory.
func relImport(module, dir string) string {
	if dir == "." {
		return module
	}
	return path.Join(module, dir)
}
