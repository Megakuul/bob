package gen

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// GeneratorModule is what a code generator that loads Go packages (gqlgen, §03.6.2) needs of
// the module it generates into: the module path and Go version, and every third-party module
// of the module's build list that provides packages, unpacked in the store. The generator
// job lays them out as vendor/ so the go command runs offline (-mod=vendor).
type GeneratorModule struct {
	Path    string // module path
	Go      string // the module's go directive in bob.lock ("1.26.0")
	Modules []VendorModule
}

// VendorModule is one third-party module of a GeneratorModule.
type VendorModule struct {
	Path, Version string
	Tree          string   // store path of the unpacked module (R-FW-063)
	Packages      []string // import paths of its packages (directories with .go files), sorted
}

// GeneratorModuleOf returns the GeneratorModule of the workspace module modPath from bob.lock
// (its build list and go directive), so it is the same whether or not the module itself is in
// the graph (bob build //svc:gen-graph). req is the generator's own PlanRequest; its Locks
// carry the Go section.
func GeneratorModuleOf(ctx context.Context, req engine.PlanRequest, modPath string) (gm GeneratorModule, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("go planner: internal error: %v", v)
		}
	}()
	gm = GeneratorModule{Path: modPath}
	r := req
	r.Lock = req.Locks[lock.SectionGo]
	pl := newPlanner(ctx, r)
	if err := pl.lockModules(); err != nil {
		return gm, err
	}
	sel, ok := pl.selected[modPath]
	if !ok || pl.lock == nil {
		return gm, diag.New(1012, "what", fmt.Sprintf("Go module %s is not locked", modPath))
	}
	gm.Go = pl.lock.Workspaces[modPath].Go
	for _, p := range sortedKeys(sel) {
		t := pl.third[p+"@"+sel[p]]
		if t == nil {
			continue // in the build list for its go.mod only
		}
		host, err := pl.moduleTree(t)
		if err != nil {
			return gm, err
		}
		pkgs, err := modulePackages(host, t.Path)
		if err != nil {
			return gm, fmt.Errorf("module %s@%s: %w", t.Path, t.Version, err)
		}
		gm.Modules = append(gm.Modules, VendorModule{Path: t.Path, Version: t.Version, Tree: pl.storeOf[host], Packages: pkgs})
	}
	return gm, nil
}

// maxModulePackages bounds the packages bob lists of one module (hostile input: a module tree
// comes from a proxy).
const maxModulePackages = 100000

// modulePackages lists the import paths of the directories of the module tree at root that
// hold .go files, as the go command sees them: testdata and directories starting with "." or
// "_" are not packages.
func modulePackages(root, modPath string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		ip := modPath
		if rel != "." {
			ip += "/" + filepath.ToSlash(rel)
		}
		if len(out) == 0 || out[len(out)-1] != ip {
			out = append(out, ip)
		}
		if len(out) > maxModulePackages {
			return fmt.Errorf("more than %d packages", maxModulePackages)
		}
		return nil
	})
	sort.Strings(out)
	return compactStrings(out), err
}

func compactStrings(s []string) []string {
	var out []string
	for _, x := range s {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}

// ModuleGoVersion is the go directive bob.lock records for the workspace module modPath
// ("1.26.0"): generators that only need a go.mod (oapi-codegen reads the go version) use it.
func ModuleGoVersion(req engine.PlanRequest, modPath string) (string, error) {
	sec := req.Locks[lock.SectionGo]
	if len(sec) > 0 {
		if v, err := lock.DecodeSection(lock.SectionGo, sec); err == nil {
			if bl, ok := v.(*lock.GoSection).Workspaces[modPath]; ok && bl.Go != "" {
				return bl.Go, nil
			}
		}
	}
	return "", diag.New(1012, "what", fmt.Sprintf("Go module %s is not locked", modPath))
}
