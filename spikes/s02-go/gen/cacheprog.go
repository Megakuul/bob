package gen

// Variant V3: one derivation per Go package that runs plain `go build` with GOCACHEPROG set to
// bob-gocacheprog. Dependency derivations' outputs are read-only cache layers; each derivation's
// output holds only the cache entries it added. Modules are laid out as vendor/ (no module cache,
// no network, no go.sum needed inside the sandbox).

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"bobspike/s02/drv"
	"bobspike/s02/gopkg"
)

func (g *Gen) cacheProgDrv(src string) (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.goToolchain(e, false); err != nil {
		return nil, err
	}
	e.srcs[src] = true
	s := fmt.Sprintf("export HOME=$TMPDIR GOCACHE=$TMPDIR/cache GOPATH=$TMPDIR/gopath GOTOOLCHAIN=local GOFLAGS= GOENV=off CGO_ENABLED=0\ncd %s\nmkdir -p $out/bin\ngo build -trimpath -o $out/bin/bob-gocacheprog main.go\n", src)
	return g.finish("bob-gocacheprog", e, s, nil)
}

// cpEnv is the environment every V3 derivation shares: it must be identical, because cmd/go
// hashes CGO_* variables into the action IDs of every cgo package (std included).
func (g *Gen) cpEnv(gb *GoBuild, e *env) string {
	cgo := "0"
	var incs, libs []string
	if gb.CGo {
		cgo = "1"
		incs, libs = g.libDeps(gb.Lib, e)
	}
	return fmt.Sprintf("export HOME=$TMPDIR GOCACHE=$TMPDIR/gocache GOPATH=$TMPDIR/gopath GOTOOLCHAIN=local GOENV=off GOPROXY=off GOSUMDB=off GOFLAGS=-mod=vendor CGO_ENABLED=%s CC=gcc\nexport CGO_CFLAGS=%q CGO_LDFLAGS=%q\n",
		cgo, strings.Join(append([]string{"-O2", "-g"}, incs...), " "), strings.Join(libs, " "))
}

func (g *Gen) GoCacheProg(gb *GoBuild) (*drv.Derivation, error) {
	cp, err := g.cacheProgDrv(gb.CacheProgSrc)
	if err != nil {
		return nil, err
	}
	// std cache layer
	e := newEnv()
	if _, err := g.goToolchain(e, gb.CGo); err != nil {
		return nil, err
	}
	cpb := e.useDrv(cp) + "/bin/bob-gocacheprog"
	s := g.cpEnv(gb, e) + fmt.Sprintf("cd $TMPDIR\nGOFLAGS= GOCACHEPROG=%s BOB_CACHE_OUT=$out BOB_CACHE_STATS=1 go build -trimpath std\n", cpb)
	std, err := g.finish("go-std-cache", e, s, nil)
	if err != nil {
		return nil, err
	}
	gb.Std = std
	byIP := map[string]*drv.Derivation{}
	var final *drv.Derivation
	for _, p := range gb.Plan.Order {
		d, err := g.cpPkg(gb, p, cp, std, byIP)
		if err != nil {
			return nil, err
		}
		byIP[p.ImportPath] = d
		gb.Groups = append(gb.Groups, &Group{Key: "pkg:" + p.ImportPath, Pkgs: []*gopkg.Pkg{p}, Drv: d})
		g.Labels[d.DrvPath] = gb.Lib.Label
		if p == gb.Plan.Main {
			final = d
		}
	}
	return final, nil
}

func (g *Gen) cpPkg(gb *GoBuild, p *gopkg.Pkg, cp, std *drv.Derivation, byIP map[string]*drv.Derivation) (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.goToolchain(e, gb.CGo); err != nil {
		return nil, err
	}
	cpb := e.useDrv(cp) + "/bin/bob-gocacheprog"
	// transitive closure (sources + cache layers)
	trans := map[string]bool{}
	var walk func(ip string)
	walk = func(ip string) {
		if trans[ip] {
			return
		}
		trans[ip] = true
		for _, i := range gb.Plan.Pkgs[ip].Imports {
			walk(i)
		}
	}
	walk(p.ImportPath)
	ips := make([]string, 0, len(trans))
	for ip := range trans {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	layers := []string{e.useDrv(std)}
	for _, ip := range ips {
		if ip != p.ImportPath {
			layers = append(layers, e.useDrv(byIP[ip])+"/cache")
		}
	}
	var s strings.Builder
	s.WriteString(g.cpEnv(gb, e))
	main := gb.Lib.Go
	fmt.Fprintf(&s, "w=$TMPDIR/w; mkdir -p $w/vendor\ncd $w\ncat > go.mod <<'EOF'\n%sEOF\n", GoModVendor(gb))
	// vendor/modules.txt + symlinks to unpacked modules
	usedPkgs := map[string][]string{}
	for _, q := range gb.Plan.Order {
		if !q.Mod.Main {
			usedPkgs[q.Mod.Path] = append(usedPkgs[q.Mod.Path], q.ImportPath)
		}
	}
	s.WriteString("cat > vendor/modules.txt <<'EOF'\n")
	for _, r := range main.Requires {
		m := modByPath(gb, r.Path)
		fmt.Fprintf(&s, "# %s %s\n## explicit; go %s\n", r.Path, r.Version, m.Lang)
		ps := usedPkgs[r.Path]
		sort.Strings(ps)
		for _, ip := range ps {
			s.WriteString(ip + "\n")
		}
	}
	s.WriteString("EOF\n")
	for _, r := range main.Requires {
		d := gb.ModDrv[r.Path]
		fmt.Fprintf(&s, "mkdir -p vendor/%s; ln -s %s vendor/%s\n", path.Dir(r.Path), path.Join(e.useDrv(d), r.Path+"@"+r.Version), r.Path)
	}
	// workspace packages of the closure, one store path each
	for _, ip := range ips {
		q := gb.Plan.Pkgs[ip]
		if !q.Mod.Main {
			continue
		}
		src, err := g.goSrc(gb, e, q)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&s, "mkdir -p %s; ln -s %s %s\n", path.Dir("./"+q.Rel), src, "./"+q.Rel)
	}
	target := "./" + p.Rel
	if !p.Mod.Main {
		target = p.ImportPath
	}
	outFlag := ""
	if p == gb.Plan.Main {
		outFlag = "-o $out/bin/" + main.Binary
		s.WriteString("mkdir -p $out/bin\n")
	}
	fmt.Fprintf(&s, "GOCACHEPROG=%s BOB_CACHE_OUT=$out/cache BOB_CACHE_IN=%s BOB_CACHE_STATS=1 go build -trimpath %s %s\n",
		cpb, strings.Join(layers, ":"), outFlag, target)
	if p == gb.Plan.Main {
		fmt.Fprintf(&s, "rm -rf $out/cache\n")
	}
	return g.finish("gocp-"+sanitize(p.ImportPath), e, s.String(), nil)
}

func modByPath(gb *GoBuild, mp string) *gopkg.Module {
	for _, m := range gb.Plan.Modules {
		if m.Path == mp {
			return m
		}
	}
	return &gopkg.Module{Lang: "1.16"}
}

// GoModVendor is the go.mod used inside V3 sandboxes (same content as the generated go.mod).
func GoModVendor(gb *GoBuild) string { return GoMod(gb.Lib) }
