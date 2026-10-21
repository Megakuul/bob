// Package gen turns a bob.lock npm section into derivations: one fixed-output fetch per tarball,
// one derivation per package instance (isolated layout, cycles collapsed), one node_modules
// derivation per importer, and builders (tsc, Vite, esbuild bundle, Bun).
package gen

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"bobspike/s04/drv"
	"bobspike/s04/nixidx"
	"bobspike/s04/npm"
)

// Quirk is one entry of bob's curated quirks table (R-NPM-005).
type Quirk struct {
	Package string            // npm package name
	Range   string            // versions the entry applies to
	Why     string            // justification (required)
	Tools   []string          // nixpkgs attributes the lifecycle script needs
	Env     map[string]string // extra env for the lifecycle script; "@attr" expands to a nixpkgs out path
	Skip    bool              // never run the script (it only downloads something bob provides)
	Test    string            // command run inside the package dir after the build as the entry's test
}

var Quirks = []Quirk{
	{Package: "better-sqlite3", Range: ">=8", Why: "install script tries a prebuilt download (prebuild-install) before node-gyp; the sandbox has no network, so go straight to the source build",
		Env: map[string]string{"npm_config_build_from_source": "true"},
		Test: `@node -e "const D=require('./');const db=new D(':memory:');console.log(db.prepare('select sqlite_version() v').get().v)"`},
	{Package: "esbuild", Range: ">=0.15", Why: "postinstall only re-checks the platform binary that the @esbuild/<platform> optional dependency already provides", Skip: true},
}

type Gen struct {
	Ix       *nixidx.Index
	Lock     *npm.Lock
	Plat     npm.Platform
	Allow    map[string]bool // lifecycle allowlist from bob.go (package names)
	Builder  string          // store path of builder/ (bins.mjs, lifecycle.mjs)
	Order    []*drv.Derivation
	Labels   map[string]string
	Notes    []string
	fetches  map[string]*drv.Derivation
	units    map[string]*unit // instance key -> unit
	skipped  map[string]bool
	scc      map[string][]string
}

type unit struct {
	d    *drv.Derivation
	dirs map[string]string // instance key -> package dir inside d's output
}

func New(ix *nixidx.Index, lock *npm.Lock, plat npm.Platform, allow map[string]bool, builder string) *Gen {
	return &Gen{Ix: ix, Lock: lock, Plat: plat, Allow: allow, Builder: builder, Labels: map[string]string{},
		fetches: map[string]*drv.Derivation{}, units: map[string]*unit{}, skipped: map[string]bool{}}
}

func platOK(list []string, v string) bool {
	if len(list) == 0 || v == "" {
		return true
	}
	allowed, anyPos := false, false
	for _, x := range list {
		if strings.HasPrefix(x, "!") {
			if x[1:] == v {
				return false
			}
			continue
		}
		anyPos = true
		if x == v {
			allowed = true
		}
	}
	return allowed || !anyPos
}

// Installable reports whether a package instance belongs on the target platform.
func (g *Gen) Installable(p *npm.LockPkg) bool {
	return platOK(p.OS, g.Plat.OS) && platOK(p.CPU, g.Plat.CPU) && platOK(p.Libc, g.Plat.Libc)
}

func StoreName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("+-._?=", r):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s = strings.TrimLeft(b.String(), ".-")
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}

// q double-quotes a path; $out stays expandable. Package names are validated (no quotes, $, `).
func q(s string) string { return `"` + strings.NewReplacer(`"`, `\"`, "`", "\\`", `\`, `\\`).Replace(s) + `"` }

func (g *Gen) out(attr string) string { return g.Ix.Attrs[attr].Outputs["out"] }

func (g *Gen) Fetch(p *npm.LockPkg) (*drv.Derivation, error) {
	k := p.Name + "@" + p.Version
	if d := g.fetches[k]; d != nil {
		return d, nil
	}
	algo, b64, ok := strings.Cut(p.Integrity, "-")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if strings.HasPrefix(p.Integrity, "sha1-hex:") {
		algo, ok = "sha1", true
		raw, err = hex.DecodeString(strings.TrimPrefix(p.Integrity, "sha1-hex:"))
	}
	if !ok || err != nil || (algo != "sha512" && algo != "sha256" && algo != "sha1") {
		return nil, fmt.Errorf("%s: unusable integrity %q in bob.lock", k, p.Integrity)
	}
	url := p.Tarball
	if url == "" {
		url = "https://registry.npmjs.org/" + p.Name + "/-/" + path.Base(p.Name) + "-" + p.Version + ".tgz"
	}
	name := StoreName("npm-" + p.Name + "-" + p.Version + ".tgz")
	sri := algo + "-" + base64.StdEncoding.EncodeToString(raw)
	d := &drv.Derivation{Name: name, System: "builtin", Builder: "builtin:fetchurl",
		Env: map[string]string{"name": name, "system": "builtin", "builder": "builtin:fetchurl",
			"url": url, "urls": url, "outputHash": sri, "outputHashAlgo": "",
			"outputHashMode": "flat", "preferLocalBuild": "1", "executable": "", "unpack": ""},
		FixedAlgo: algo, FixedMode: "flat", FixedHash: raw}
	if err := g.finish(d, "//npm:"+k+".tgz"); err != nil {
		return nil, err
	}
	g.fetches[k] = d
	return d, nil
}

func (g *Gen) finish(d *drv.Derivation, label string) error {
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	g.Labels[d.DrvPath] = label
	return nil
}

type script struct {
	g      *Gen
	inputs map[string][]string
	srcs   map[string]bool
	path   []string
	env    map[string]string
	b      strings.Builder
}

func (g *Gen) newScript(tools ...string) *script {
	s := &script{g: g, inputs: map[string][]string{}, srcs: map[string]bool{}, env: map[string]string{}}
	for _, t := range append([]string{"bash", "coreutils"}, tools...) {
		s.tool(t)
	}
	return s
}

func (s *script) tool(attr string) string {
	a := s.g.Ix.Attrs[attr]
	s.inputs[a.DrvPath] = []string{"out"}
	o := a.Outputs["out"]
	s.path = append(s.path, o+"/bin")
	return o
}

func (s *script) dep(d *drv.Derivation) string { s.inputs[d.DrvPath] = []string{"out"}; return d.OutPath }
func (s *script) src(p string) string         { s.srcs[p] = true; return p }
func (s *script) f(format string, a ...any)    { fmt.Fprintf(&s.b, format, a...) }

const prelude = "trap 'echo \"bob-builder: command failed (exit $?): $BASH_COMMAND\" >&2' ERR\n"

func (s *script) drv(name, label string) (*drv.Derivation, error) {
	g := s.g
	bash := g.out("bash") + "/bin/bash"
	seen := map[string]bool{}
	var p []string
	for _, x := range s.path {
		if !seen[x] {
			seen[x] = true
			p = append(p, x)
		}
	}
	env := map[string]string{"name": name, "system": g.Ix.System, "builder": bash, "PATH": strings.Join(p, ":")}
	for k, v := range s.env {
		env[k] = v
	}
	d := &drv.Derivation{Name: name, System: g.Ix.System, Builder: bash,
		Args: []string{"-euo", "pipefail", "-c", prelude + s.b.String()}, Env: env, InputDrvs: s.inputs}
	for x := range s.srcs {
		d.InputSrcs = append(d.InputSrcs, x)
	}
	sort.Strings(d.InputSrcs)
	return d, g.finish(d, label)
}

// deps returns the installable dependency edges of an instance.
func (g *Gen) deps(k string) map[string]string {
	p := g.Lock.Packages[k]
	out := map[string]string{}
	for a, d := range p.Deps {
		if strings.HasPrefix(d, "link:") {
			continue // workspace links of registry packages are not supported in the spike
		}
		if dp := g.Lock.Packages[d]; dp != nil && g.Installable(dp) {
			out[a] = d
		}
	}
	return out
}

// sccs computes strongly connected components (Tarjan) over installable instances.
func (g *Gen) sccs() {
	g.scc = map[string][]string{}
	idx, low := map[string]int{}, map[string]int{}
	on := map[string]bool{}
	var st []string
	n := 0
	var visit func(v string)
	visit = func(v string) {
		idx[v], low[v] = n, n
		n++
		st = append(st, v)
		on[v] = true
		for _, w := range sortedVals(g.deps(v)) {
			if _, ok := idx[w]; !ok {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], idx[w])
			}
		}
		if low[v] == idx[v] {
			var comp []string
			for {
				w := st[len(st)-1]
				st = st[:len(st)-1]
				on[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			sort.Strings(comp)
			for _, w := range comp {
				g.scc[w] = comp
			}
		}
	}
	for _, k := range sortedKeys(g.Lock.Packages) {
		if _, ok := idx[k]; !ok && g.Installable(g.Lock.Packages[k]) {
			visit(k)
		}
	}
}

func (g *Gen) quirk(p *npm.LockPkg) *Quirk {
	for i := range Quirks {
		qk := &Quirks[i]
		if qk.Package != p.Name {
			continue
		}
		r, err := npm.ParseRange(qk.Range)
		v, err2 := npm.ParseVersion(p.Version)
		if err == nil && err2 == nil && r.Match(v) {
			return qk
		}
	}
	return nil
}

// Unit returns the derivation holding instance k (and the rest of its cycle, if any).
func (g *Gen) Unit(k string) (*unit, error) {
	if g.scc == nil {
		g.sccs()
	}
	if u := g.units[k]; u != nil {
		return u, nil
	}
	comp := g.scc[k]
	inComp := map[string]bool{}
	for _, c := range comp {
		inComp[c] = true
	}
	// dependencies outside the component first
	for _, c := range comp {
		for _, d := range sortedVals(g.deps(c)) {
			if !inComp[d] {
				if _, err := g.Unit(d); err != nil {
					return nil, err
				}
			}
		}
	}
	u := &unit{dirs: map[string]string{}}
	for i, c := range comp {
		p := g.Lock.Packages[c]
		base := "$out"
		if len(comp) > 1 {
			base = fmt.Sprintf("$out/%d", i)
		}
		u.dirs[c] = base + "/node_modules/" + p.Name
	}
	s := g.newScript("gnutar", "gzip", "findutils")
	needNode := false
	for _, c := range comp {
		p := g.Lock.Packages[c]
		f, err := g.Fetch(p)
		if err != nil {
			return nil, err
		}
		dir := u.dirs[c]
		s.f("mkdir -p %s\ntar -xzf %s --strip-components=1 -C %s --no-same-owner --no-same-permissions --warning=no-unknown-keyword\nchmod -R u+rwX,go+rX %s\n",
			q(dir), s.dep(f), q(dir), q(dir))
		s.f("[ -f %s/package.json ] || { echo \"bob: %s: tarball has no package.json\" >&2; exit 1; }\n", q(dir), c)
		root := strings.TrimSuffix(dir, "/"+p.Name)
		for _, a := range sortedKeys(g.deps(c)) {
			if a == p.Name {
				continue
			}
			d := g.deps(c)[a]
			var target string
			if inComp[d] {
				target = u.dirs[d]
			} else {
				du := g.units[d]
				target = strings.Replace(du.dirs[d], "$out", s.dep(du.d), 1)
			}
			s.f("mkdir -p \"$(dirname %s)\"\nln -s %s %s\n", q(root+"/"+a), q(target), q(root+"/"+a))
		}
		if p.HasInstallScript {
			qk := g.quirk(p)
			switch {
			case qk != nil && qk.Skip:
				g.Notes = append(g.Notes, fmt.Sprintf("%s: install script skipped by quirk (%s)", c, qk.Why))
			case !g.Allow[p.Name]:
				g.Notes = append(g.Notes, fmt.Sprintf("%s: install script denied (not in the lifecycle allowlist)", c))
			default:
				needNode = true
				node := s.tool("nodejs")
				// bob's native toolchain for allowed scripts (R-NPM-004): node-gyp needs python, make, cc
				for _, t := range []string{"python3", "gnumake", "stdenv.cc", "gnused", "gnugrep"} {
					s.tool(t)
				}
				if qk != nil {
					for _, t := range qk.Tools {
						s.tool(t)
					}
					for kk, v := range qk.Env {
						s.env[kk] = v
					}
				}
				// deps' bins on PATH (e.g. prebuild-install), node-gyp from the npm bundled with nixpkgs node
				s.f("export HOME=$TMPDIR npm_config_nodedir=%s npm_config_cache=$TMPDIR/npm-cache\n", node)
				s.f("mkdir -p $TMPDIR/bin\nprintf '#!%s\\nexec %s/bin/node %s/lib/node_modules/npm/node_modules/node-gyp/bin/node-gyp.js \"$@\"\\n' > $TMPDIR/bin/node-gyp\nchmod +x $TMPDIR/bin/node-gyp\n",
					g.out("bash")+"/bin/bash", node, node)
				var depDirs []string
				for _, a := range sortedKeys(g.deps(c)) {
					depDirs = append(depDirs, q(root+"/"+a))
				}
				s.f("%s/bin/node %s/bins.mjs $TMPDIR/bin %s/bin/bash %s/bin/node %s\n", node, s.src(g.Builder), g.out("bash"), node, strings.Join(depDirs, " "))
				s.f("export PATH=$TMPDIR/bin:$PATH npm_package_name=%s npm_package_version=%s npm_lifecycle_event=install\n", q(p.Name), q(p.Version))
				s.f("( cd %s && %s/bin/node %s/lifecycle.mjs . | while read -r line; do cmd=$(%s/bin/node -e 'console.log(JSON.parse(process.argv[1]))' \"$line\"); echo \"bob: running install script of %s: $cmd\" >&2; %s/bin/bash -euo pipefail -c \"$cmd\" </dev/null; done )\n",
					q(dir), node, g.Builder, node, c, g.out("bash"))
				if qk != nil && qk.Test != "" {
					s.f("( cd %s && %s )\n", q(dir), strings.Replace(qk.Test, "@node", node+"/bin/node", 1))
				}
				// drop build intermediates that pin the toolchain into the runtime closure
				s.f("if [ -d %[1]s/build/Release ]; then mkdir $TMPDIR/keep && find %[1]s/build/Release -maxdepth 1 -name '*.node' -exec mv {} $TMPDIR/keep/ \\; && rm -rf %[1]s/build && mkdir -p %[1]s/build/Release && mv $TMPDIR/keep/* %[1]s/build/Release/; fi\n", q(dir))
				g.Notes = append(g.Notes, fmt.Sprintf("%s: install script allowed, runs in the sandbox", c))
			}
		}
	}
	_ = needNode
	name := StoreName("npm-" + strings.ReplaceAll(comp[0], "(", "_"))
	if len(comp) > 1 {
		name = StoreName(fmt.Sprintf("npm-cycle-%d-%s", len(comp), comp[0]))
	}
	d, err := s.drv(name, "//npm:"+comp[0])
	if err != nil {
		return nil, err
	}
	u.d = d
	for _, c := range comp {
		g.units[c] = u
	}
	return u, nil
}

// Dir returns the store directory of an instance (after Unit).
func (g *Gen) Dir(k string) string {
	u := g.units[k]
	return strings.Replace(u.dirs[k], "$out", u.d.OutPath, 1)
}

// NodeModules builds the importer's node_modules (direct deps as symlinks, .bin wrappers).
// links maps workspace paths to derivations whose output has node_modules/<name>.
func (g *Gen) NodeModules(importer string, links map[string]*drv.Derivation, name string) (*drv.Derivation, error) {
	li := g.Lock.Importers[importer]
	if li == nil {
		return nil, fmt.Errorf("bob.lock has no importer %q", importer)
	}
	s := g.newScript("nodejs")
	s.f("mkdir -p $out/node_modules\n")
	var dirs []string
	for _, a := range sortedKeys(li.Deps) {
		k := li.Deps[a]
		var target string
		if strings.HasPrefix(k, "link:") {
			ld := links[strings.TrimPrefix(k, "link:")]
			if ld == nil {
				return nil, fmt.Errorf("workspace package %s is not built", k)
			}
			target = s.dep(ld) + "/node_modules/" + a
		} else {
			p := g.Lock.Packages[k]
			if !g.Installable(p) {
				continue
			}
			u, err := g.Unit(k)
			if err != nil {
				return nil, err
			}
			s.dep(u.d)
			target = g.Dir(k)
		}
		s.f("mkdir -p \"$(dirname $out/node_modules/%s)\"\nln -s %s $out/node_modules/%s\n", a, q(target), a)
		dirs = append(dirs, "$out/node_modules/"+a)
	}
	node := g.out("nodejs")
	s.f("node %s/bins.mjs $out/node_modules/.bin %s/bin/bash %s/bin/node %s\n", s.src(g.Builder), g.out("bash"), node, strings.Join(dirs, " "))
	return s.drv(name, "//"+importer+":node_modules")
}

// WorkspaceLib packages a workspace library (sources at src) with its own deps linked next to it.
func (g *Gen) WorkspaceLib(importer, pkgName, src, peersFrom string) (*drv.Derivation, error) {
	li := g.Lock.Importers[importer]
	s := g.newScript()
	dir := "$out/node_modules/" + pkgName
	s.f("mkdir -p %s\ncp -r %s/. %s/\nchmod -R u+w %s\n", dir, s.src(src), dir, dir)
	dev := map[string]bool{}
	for _, d := range li.Dev {
		dev[d] = true
	}
	// runtime deps of the library; peers come from the consumer (peersFrom importer)
	need := map[string]string{}
	for a, k := range li.Deps {
		if !dev[a] {
			need[a] = k
		}
	}
	if pi := g.Lock.Importers[peersFrom]; pi != nil {
		for a, k := range pi.Deps {
			if _, own := need[a]; !own && dev[a] {
				need[a] = k // a peer the lib uses only as devDep: link the consumer's instance
			}
		}
	}
	for _, a := range sortedKeys(need) {
		k := need[a]
		if strings.HasPrefix(k, "link:") {
			continue
		}
		u, err := g.Unit(k)
		if err != nil {
			return nil, err
		}
		s.dep(u.d)
		s.f("mkdir -p \"$(dirname $out/node_modules/%s)\"\nln -s %s $out/node_modules/%s\n", a, q(g.Dir(k)), a)
	}
	return s.drv(StoreName("ws-"+pkgName), "//"+importer+":lib")
}

// Hoisted builds an npm-style hoisted node_modules by copying (extracting) every package into
// one derivation. Only used to compare layouts; no lifecycle scripts.
func (g *Gen) Hoisted(importer, name string) (*drv.Derivation, error) {
	li := g.Lock.Importers[importer]
	place := map[string]string{} // path -> instance key
	type item struct{ parent, alias, key string }
	var queue []item
	for _, a := range sortedKeys(li.Deps) {
		if !strings.HasPrefix(li.Deps[a], "link:") {
			queue = append(queue, item{"", a, li.Deps[a]})
		}
	}
	placed := map[string]string{} // key -> path where its deps get resolved from
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		p := g.Lock.Packages[it.key]
		if p == nil || !g.Installable(p) {
			continue
		}
		// walk up from parent: is a matching package visible already?
		vis := ""
		for dir := it.parent; ; {
			cand := strings.TrimPrefix(dir+"/node_modules/"+it.alias, "/")
			if k, ok := place[cand]; ok {
				vis = k
				break
			}
			if dir == "" {
				break
			}
			if i := strings.LastIndex(dir, "/node_modules/"); i >= 0 {
				dir = dir[:i]
			} else {
				dir = ""
			}
		}
		if vis != "" && strings.SplitN(vis, "(", 2)[0] == strings.SplitN(it.key, "(", 2)[0] {
			continue
		}
		at := "node_modules/" + it.alias
		if _, taken := place[at]; taken || vis != "" {
			at = strings.TrimPrefix(it.parent+"/node_modules/"+it.alias, "/")
		}
		if _, taken := place[at]; taken {
			continue
		}
		place[at] = it.key
		for _, a := range sortedKeys(g.deps(it.key)) {
			queue = append(queue, item{at, a, g.deps(it.key)[a]})
		}
		placed[it.key] = at
	}
	s := g.newScript("gnutar", "gzip", "nodejs")
	var dirs []string
	for _, at := range sortedKeys(place) {
		p := g.Lock.Packages[place[at]]
		f, err := g.Fetch(p)
		if err != nil {
			return nil, err
		}
		s.f("mkdir -p $out/%s && tar -xzf %s --strip-components=1 -C $out/%s --no-same-owner --no-same-permissions --warning=no-unknown-keyword\n", at, s.dep(f), at)
		if strings.Count(at, "node_modules/") == 1 {
			if _, direct := li.Deps[strings.TrimPrefix(at, "node_modules/")]; direct {
				dirs = append(dirs, "$out/"+at)
			}
		}
	}
	s.f("chmod -R u+rwX,go+rX $out\nnode %s/bins.mjs $out/node_modules/.bin %s/bin/bash %s/bin/node %s\n", s.src(g.Builder), g.out("bash"), g.out("nodejs"), strings.Join(dirs, " "))
	return s.drv(name, "//"+importer+":node_modules-hoisted")
}

// Build runs a builder command over the project sources with node_modules materialised as a
// writable directory of symlinks (tools such as Vite write caches under node_modules/).
func (g *Gen) Build(name, label, src string, nm *drv.Derivation, tools []string, cmd string) (*drv.Derivation, error) {
	s := g.newScript(append([]string{"nodejs", "findutils", "gnused", "gnugrep"}, tools...)...)
	for _, t := range tools {
		s.env["tool_"+strings.NewReplacer("-", "_", ".", "_").Replace(t)] = g.out(t)
	}
	s.f("export HOME=$TMPDIR\nmkdir -p $TMPDIR/w && cd $TMPDIR/w\ncp -r %s/. .\nchmod -R u+w .\nmkdir node_modules\ncp -a %s/node_modules/. node_modules/\nexport PATH=$PWD/node_modules/.bin:$PATH\n", s.src(src), s.dep(nm))
	s.f("%s\n", cmd)
	return s.drv(name, label)
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedVals(m map[string]string) []string {
	var vs []string
	for _, k := range sortedKeys(m) {
		vs = append(vs, m[k])
	}
	return vs
}
