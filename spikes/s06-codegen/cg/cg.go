// Package cg is spike S6's stand-in for bob's codegen API: typed constructors for code
// generators (proto/connect, GraphQL, OpenAPI) and the Go/TS packages that consume them,
// computed directly into derivations (backend B). Names mirror the SPEC proposal.
package cg

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"bobspike/s06/drv"
	"bobspike/s06/nixidx"
)

// B is the build context while bob.go is evaluated.
type B struct {
	Root    string            // repo root (absolute)
	ToolDir string            // where bob-shipped tool locks live (absolute)
	GoLock  map[string]string // "path@ver.zip|.mod" -> SRI sha256 (bob.lock stand-in)
	Pkgs    []*Pkg
	byLabel map[string]*Pkg
	memo    map[string]any
}

type Pkg struct {
	Label     string
	Kind      string // fetch | codegen | go_binaries | ts_build | node_modules | goproxy
	DefinedAt string
	Deps      []*Pkg
	Srcs      []SrcSpec
	Out       string // codegen: repo-relative directory bob materialises into
	Tools     []string
	Checks    []VersionCheck
	build     func(c *Ctx) (*drv.Derivation, error)
	Drv       *drv.Derivation
}

// SrcSpec is a local source the package reads; Include filters it (nil = all).
type SrcSpec struct {
	Key     string
	Dir     string // repo-relative or absolute
	Include func(rel string, isDir bool) bool
}

// VersionCheck: a generator whose output needs a matching runtime library.
type VersionCheck struct {
	Tool, ToolAttr string // e.g. gqlgen
	Runtime        string // e.g. github.com/99designs/gqlgen (go.mod) or @bufbuild/protobuf (lock)
	RuntimeVer     string
	Rule           string // "equal" | "atleast"
	Where          string // file the runtime version comes from
}

func New(root, toolDir string, goLock map[string]string) *B {
	return &B{Root: root, ToolDir: toolDir, GoLock: goLock, byLabel: map[string]*Pkg{}, memo: map[string]any{}}
}

func definedAt() string {
	for i := 2; i < 8; i++ {
		_, f, l, ok := runtime.Caller(i)
		if !ok {
			break
		}
		if !strings.Contains(f, "/cg/") {
			return fmt.Sprintf("%s:%d", filepath.Base(f), l)
		}
	}
	return "?"
}

func (b *B) add(p *Pkg) *Pkg {
	if q, ok := b.byLabel[p.Label]; ok {
		return q
	}
	if p.DefinedAt == "" {
		p.DefinedAt = definedAt()
	}
	b.byLabel[p.Label] = p
	b.Pkgs = append(b.Pkgs, p)
	return p
}

func (b *B) Get(label string) *Pkg { return b.byLabel[label] }

func (b *B) abs(dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(b.Root, dir)
}

var common = []string{"bash", "coreutils", "gnused", "gnugrep", "findutils"}

// ---------------------------------------------------------------- derivation context

type Ctx struct {
	B    *B
	Ix   *nixidx.Index
	Srcs map[string]string // SrcSpec.Key -> store path
	// Order lists derivations in creation order (deps first); Labels maps drv path -> label.
	Order  []*drv.Derivation
	Labels map[string]string
	// CA maps a codegen package to a content-addressed copy of its output (early cutoff mode):
	// dependents then reference the content, not the producing derivation.
	CA map[*Pkg]string
}

func (c *Ctx) Drv(p *Pkg) (*drv.Derivation, error) {
	if p.Drv != nil {
		return p.Drv, nil
	}
	for _, d := range p.Deps {
		if _, err := c.Drv(d); err != nil {
			return nil, err
		}
	}
	d, err := p.build(c)
	if err != nil {
		return nil, fmt.Errorf("%s (defined at %s): %w", p.Label, p.DefinedAt, err)
	}
	p.Drv = d
	c.Labels[d.DrvPath] = p.Label
	return d, nil
}

type env struct {
	c      *Ctx
	inputs map[string][]string
	srcs   map[string]bool
	path   []string
	extra  map[string]string
}

func (c *Ctx) env(tools []string) (*env, error) {
	e := &env{c: c, inputs: map[string][]string{}, srcs: map[string]bool{}, extra: map[string]string{}}
	for _, t := range append(append([]string{}, common...), tools...) {
		if _, err := e.attr(t); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *env) attr(name string) (string, error) {
	a := e.c.Ix.Attrs[name]
	if a == nil || a.DrvPath == "" {
		return "", fmt.Errorf("nixpkgs package %s not resolved", name)
	}
	out := a.Outputs["out"]
	e.inputs[a.DrvPath] = []string{"out"}
	e.path = append(e.path, out+"/bin")
	return out, nil
}

func (e *env) dep(p *Pkg) string {
	if ca, ok := e.c.CA[p]; ok {
		e.srcs[ca] = true
		return ca
	}
	e.inputs[p.Drv.DrvPath] = []string{"out"}
	return p.Drv.OutPath
}

func (e *env) src(key string) string {
	sp := e.c.Srcs[key]
	e.srcs[sp] = true
	return sp
}

const prelude = "trap 'echo \"bob-builder: command failed (exit $?): $BASH_COMMAND\" >&2' ERR\n"

func (e *env) finish(name, script string) (*drv.Derivation, error) {
	ix := e.c.Ix
	bash := ix.Attrs["bash"].Outputs["out"] + "/bin/bash"
	envm := map[string]string{"name": name, "system": ix.System, "builder": bash, "PATH": strings.Join(uniq(e.path), ":")}
	for k, v := range e.extra {
		envm[k] = v
	}
	d := &drv.Derivation{Name: name, System: ix.System, Builder: bash,
		Args: []string{"-euo", "pipefail", "-c", prelude + script}, Env: envm, InputDrvs: e.inputs}
	for s := range e.srcs {
		d.InputSrcs = append(d.InputSrcs, s)
	}
	sort.Strings(d.InputSrcs)
	if err := d.Finalize(ix.Cache); err != nil {
		return nil, err
	}
	ix.Cache.Set(d.DrvPath, d.Modulo())
	e.c.Order = append(e.c.Order, d)
	return d, nil
}

func (c *Ctx) fetch(name, url, sri string) (*drv.Derivation, error) {
	algo, b64, ok := strings.Cut(sri, "-")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil || (algo != "sha256" && algo != "sha512") {
		return nil, fmt.Errorf("bad hash %q for %s", sri, url)
	}
	d := &drv.Derivation{Name: name, System: "builtin", Builder: "builtin:fetchurl",
		Env: map[string]string{"name": name, "system": "builtin", "builder": "builtin:fetchurl",
			"url": url, "urls": url, "outputHash": sri, "outputHashAlgo": "",
			"outputHashMode": "flat", "preferLocalBuild": "1", "executable": "", "unpack": ""},
		FixedAlgo: algo, FixedMode: "flat", FixedHash: raw}
	if err := d.Finalize(c.Ix.Cache); err != nil {
		return nil, err
	}
	c.Ix.Cache.Set(d.DrvPath, d.Modulo())
	c.Order = append(c.Order, d)
	return d, nil
}

func storeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("+-._?=", r):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.TrimLeft(b.String(), ".-")
}

func sanitize(label string) string { return storeName(strings.TrimPrefix(label, "//")) }

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// writeFile emits a heredoc-free file write (content passed via env var).
func (e *env) file(dst, varName, content string) string {
	e.extra[varName] = content
	return fmt.Sprintf("printf '%%s' \"$%s\" > %s\n", varName, dst)
}

// ---------------------------------------------------------------- Go modules & proxy

type GoModule struct {
	b        *B
	Dir      string // repo-relative
	Path     string // module path
	Requires map[string]string
	gens     []*Pkg
	proxy    *Pkg
}

// NewGoModule reads the module's go.mod/go.sum (generated by bob from bob.go in the real
// design; hand-written in the spike) and pins every module file through the lock.
func NewGoModule(b *B, dir, modPath string) (*GoModule, error) {
	m := &GoModule{b: b, Dir: dir, Path: modPath, Requires: map[string]string{}}
	gm, err := os.ReadFile(filepath.Join(b.abs(dir), "go.mod"))
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(string(gm), "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "require "))
		if len(f) >= 2 && strings.HasPrefix(f[1], "v") && strings.Contains(f[0], ".") {
			m.Requires[f[0]] = f[1]
		}
	}
	proxy, err := b.goProxy(m)
	if err != nil {
		return nil, err
	}
	m.proxy = proxy
	return m, nil
}

func goEscape(p string) string {
	var s strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			s.WriteByte('!')
			s.WriteRune(r + 32)
		} else {
			s.WriteRune(r)
		}
	}
	return s.String()
}

type goFile struct{ path, ver, ext string }

func (b *B) goProxy(m *GoModule) (*Pkg, error) {
	f, err := os.Open(filepath.Join(b.abs(m.Dir), "go.sum"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var files []goFile
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 3 {
			continue
		}
		gf := goFile{fs[0], fs[1], ".zip"}
		if v, ok := strings.CutSuffix(fs[1], "/go.mod"); ok {
			gf.ver, gf.ext = v, ".mod"
		}
		files = append(files, gf)
	}
	label := "//" + m.Dir + ":goproxy"
	var fetches []*Pkg
	for _, gf := range files {
		gf := gf
		key := gf.path + "@" + gf.ver + gf.ext
		sri, ok := b.GoLock[key]
		fl := "//third_party/go:" + storeName(gf.path+"@"+gf.ver+gf.ext)
		if !ok {
			return nil, fmt.Errorf("✗ %s has no pin in bob.lock\n  defined at %s\n  hint: run bob lock", key, definedAt())
		}
		url := "https://proxy.golang.org/" + goEscape(gf.path) + "/@v/" + gf.ver + gf.ext
		fetches = append(fetches, b.add(&Pkg{Label: fl, Kind: "fetch", build: func(c *Ctx) (*drv.Derivation, error) {
			return c.fetch(storeName("go-"+gf.path+"-"+gf.ver+gf.ext), url, sri)
		}}))
	}
	return b.add(&Pkg{Label: label, Kind: "goproxy", Deps: fetches, build: func(c *Ctx) (*drv.Derivation, error) {
		e, err := c.env(nil)
		if err != nil {
			return nil, err
		}
		var s strings.Builder
		for i, gf := range files {
			d := "$out/" + goEscape(gf.path) + "/@v"
			fmt.Fprintf(&s, "mkdir -p %s; cp %s %s/%s%s\n", d, e.dep(fetches[i]), d, gf.ver, gf.ext)
			fmt.Fprintf(&s, "printf '{\"Version\":\"%s\"}' > %s/%s.info; echo %s >> %s/list\n", gf.ver, d, gf.ver, gf.ver, d)
		}
		s.WriteString("for l in $(find $out -name list); do sort -u -o $l $l; done\n")
		return e.finish(sanitize(label), s.String())
	}}), nil
}

// ProxyPath is the store path of the module proxy (for gopls / go tooling outside builds).
func (m *GoModule) Proxy() *Pkg { return m.proxy }

func (m *GoModule) goEnv(e *env) string {
	return fmt.Sprintf("export GOPROXY=file://%s GOFLAGS=-mod=readonly GOSUMDB=off GOTOOLCHAIN=local GOCACHE=$TMPDIR/gocache GOMODCACHE=$TMPDIR/gomodcache GOPATH=$TMPDIR/gopath HOME=$TMPDIR CGO_ENABLED=0\n", e.dep(m.proxy))
}

// moduleSrc: the module's own sources minus every materialised codegen root (R-FW-021).
func (m *GoModule) moduleSrc(owned func() []string) SrcSpec {
	return SrcSpec{Key: "go-module:" + m.Dir, Dir: m.Dir, Include: func(rel string, isDir bool) bool {
		full := m.Dir + "/" + rel
		for _, o := range owned() {
			if full == o || strings.HasPrefix(full, o+"/") {
				return false
			}
		}
		return !(isDir && (path.Base(rel) == "node_modules" || path.Base(rel) == ".bob"))
	}}
}

// goModOnly: just go.mod + go.sum (codegen steps that type-check against the module graph).
func (m *GoModule) goModOnly() SrcSpec {
	return SrcSpec{Key: "go-mod:" + m.Dir, Dir: m.Dir, Include: func(rel string, isDir bool) bool {
		return !isDir && (rel == "go.mod" || rel == "go.sum")
	}}
}

// Binaries builds the module's main packages with every codegen output in place.
func (m *GoModule) Binaries(name string, mains ...string) *Pkg {
	label := "//" + m.Dir + ":" + name
	b := m.b
	var p *Pkg
	p = b.add(&Pkg{Label: label, Kind: "go_binaries", Tools: []string{"go"},
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			fmt.Fprintf(&s, "mkdir -p $out/bin $TMPDIR/w && cp -r %s/. $TMPDIR/w/ && chmod -R u+w $TMPDIR/w && cd $TMPDIR/w\n", e.src(p.Srcs[0].Key))
			for _, g := range m.gens {
				rel := strings.TrimPrefix(g.Out, m.Dir+"/")
				fmt.Fprintf(&s, "mkdir -p %s && cp -r %s/. %s/ && chmod -R u+w %s\n", rel, e.dep(g), rel, rel)
			}
			s.WriteString(m.goEnv(e))
			fmt.Fprintf(&s, "go build -trimpath -o $out/bin/ %s\n", strings.Join(mains, " "))
			return e.finish(sanitize(label), s.String())
		}})
	p.Deps = append(append([]*Pkg{m.proxy}, m.gens...), p.Deps...)
	p.Srcs = []SrcSpec{m.moduleSrc(func() []string { return ownedOf(m.gens) })}
	return p
}

func ownedOf(ps []*Pkg) []string {
	var o []string
	for _, p := range ps {
		o = append(o, p.Out)
	}
	return o
}

// ---------------------------------------------------------------- protobuf

// ProtoLibrary is one protobuf package: the .proto files of one directory under an import root.
type ProtoLibrary struct {
	Label string
	Root  string // import root, repo-relative (e.g. "proto")
	Dir   string // package directory below Root (e.g. "greet/v1")
	Deps  []*ProtoLibrary
	src   SrcSpec
}

func (b *B) ProtoLibrary(root, dir string, deps ...*ProtoLibrary) *ProtoLibrary {
	key := "proto:" + root + "/" + dir
	if l, ok := b.memo[key]; ok {
		return l.(*ProtoLibrary)
	}
	l := &ProtoLibrary{Label: "//" + root + "/" + dir, Root: root, Dir: dir, Deps: deps,
		src: SrcSpec{Key: key, Dir: root, Include: func(rel string, isDir bool) bool {
			if isDir {
				return rel == dir || strings.HasPrefix(dir, rel+"/") || strings.HasPrefix(rel, dir+"/")
			}
			return path.Dir(rel) == dir && strings.HasSuffix(rel, ".proto")
		}}}
	b.memo[key] = l
	return l
}

func (l *ProtoLibrary) closure() []*ProtoLibrary {
	seen := map[*ProtoLibrary]bool{}
	var out []*ProtoLibrary
	var walk func(x *ProtoLibrary)
	walk = func(x *ProtoLibrary) {
		if seen[x] {
			return
		}
		seen[x] = true
		for _, d := range x.Deps {
			walk(d)
		}
		out = append(out, x)
	}
	walk(l)
	return out
}

func goPkgName(dir string) string { // greet/v1 -> greetv1
	parts := strings.Split(dir, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + parts[len(parts)-1]
	}
	return parts[0]
}

// protoGen builds one buf generate step: the library's files plus its imports, one output dir.
func (b *B) protoGen(label, out string, lib *ProtoLibrary, tools []string, plugins string, deps []*Pkg) *Pkg {
	var p *Pkg
	libs := lib.closure()
	var srcs []SrcSpec
	for _, l := range libs {
		srcs = append(srcs, l.src)
	}
	p = b.add(&Pkg{Label: label, Kind: "codegen", Out: out, Srcs: srcs, Deps: deps, Tools: append([]string{"buf"}, tools...),
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			s.WriteString("mkdir -p $TMPDIR/w && cd $TMPDIR/w\n")
			for _, l := range libs { // lay out sources at their repo paths, so errors name repo files
				fmt.Fprintf(&s, "mkdir -p %s && cp -r %s/. %s/\n", l.Root, e.src(l.src.Key), l.Root)
			}
			s.WriteString("chmod -R u+w .\n")
			s.WriteString(e.file("buf.yaml", "bufYaml", fmt.Sprintf("version: v2\nmodules:\n  - path: %s\n", lib.Root)))
			s.WriteString(e.file("buf.gen.yaml", "bufGenYaml", "version: v2\nplugins:\n"+plugins))
			s.WriteString("export HOME=$TMPDIR BUF_CACHE_DIR=$TMPDIR/bufcache\n")
			fmt.Fprintf(&s, "buf generate --path %s/%s\n", lib.Root, lib.Dir)
			fmt.Fprintf(&s, "mkdir -p $out && if [ -d gen/%s ]; then cp -r gen/%s/. $out/; fi\n", lib.Dir, lib.Dir)
			return e.finish(sanitize(label), s.String())
		}})
	return p
}

// ConnectGo generates protobuf + connect Go code for lib into module m (gen/proto/<dir>),
// together with the Go packages of everything lib imports (memoised, R-FW-011).
func (m *GoModule) ConnectGo(lib *ProtoLibrary) *Pkg {
	key := "connect-go:" + m.Dir + ":" + lib.Label
	if p, ok := m.b.memo[key]; ok {
		return p.(*Pkg)
	}
	var deps []*Pkg
	for _, d := range lib.Deps {
		deps = append(deps, m.ConnectGo(d))
	}
	// protoc-gen-go needs an M option per file: bob, not the .proto file, decides the Go import path.
	opts := "[paths=source_relative"
	for _, l := range lib.closure() {
		for _, f := range protoFiles(m.b, l) {
			opts += fmt.Sprintf(", M%s=%s/gen/proto/%s;%s", f, m.Path, l.Dir, goPkgName(l.Dir))
		}
	}
	opts += "]"
	plugins := fmt.Sprintf("  - local: protoc-gen-go\n    out: gen\n    opt: %s\n  - local: protoc-gen-connect-go\n    out: gen\n    opt: %s\n", opts, opts)
	label := lib.Label + ":go"
	if m.Dir != "svc" {
		label += "_" + storeName(m.Dir)
	}
	p := m.b.protoGen(label, m.Dir+"/gen/proto/"+lib.Dir, lib, []string{"protoc-gen-go", "protoc-gen-connect-go"}, plugins, deps)
	p.Checks = append(p.Checks,
		VersionCheck{Tool: "protoc-gen-go", ToolAttr: "protoc-gen-go", Runtime: "google.golang.org/protobuf", RuntimeVer: m.Requires["google.golang.org/protobuf"], Rule: "atleast", Where: m.Dir + "/go.mod"},
		VersionCheck{Tool: "protoc-gen-connect-go", ToolAttr: "protoc-gen-connect-go", Runtime: "connectrpc.com/connect", RuntimeVer: m.Requires["connectrpc.com/connect"], Rule: "atleast-minor13", Where: m.Dir + "/go.mod"})
	m.gens = append(m.gens, p)
	m.b.memo[key] = p
	return p
}

func protoFiles(b *B, l *ProtoLibrary) []string {
	ents, _ := os.ReadDir(filepath.Join(b.abs(l.Root), l.Dir))
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".proto") {
			out = append(out, l.Dir+"/"+e.Name())
		}
	}
	return out
}

// ---------------------------------------------------------------- npm (lockfile-pinned) and TS

type lockFile struct {
	LockfileVersion int `json:"lockfileVersion"`
	Packages        map[string]struct {
		Version   string `json:"version"`
		Resolved  string `json:"resolved"`
		Integrity string `json:"integrity"`
		Link      bool   `json:"link"`
		HasScript bool   `json:"hasInstallScript"`
	} `json:"packages"`
}

type NodeModules struct {
	*Pkg
	Versions map[string]string // package name -> version (top-level)
}

// NodeModulesFromLock lays out node_modules exactly as package-lock.json describes it, one
// fixed-output fetch per tarball keyed by its integrity. Stand-in for S4's resolver/installer.
func (b *B) NodeModulesFromLock(label, lockPath string) (*NodeModules, error) {
	raw, err := os.ReadFile(b.abs(lockPath))
	if err != nil {
		return nil, err
	}
	var lf lockFile
	if err := json.Unmarshal(raw, &lf); err != nil {
		return nil, fmt.Errorf("%s: %w", lockPath, err)
	}
	if lf.LockfileVersion < 2 {
		return nil, fmt.Errorf("%s: lockfileVersion %d unsupported", lockPath, lf.LockfileVersion)
	}
	var keys []string
	for k, v := range lf.Packages {
		if k == "" || v.Link {
			continue
		}
		if v.HasScript {
			return nil, fmt.Errorf("%s: %s has an install script; scripts are denied (R-NPM-004)", lockPath, k)
		}
		if strings.Contains(k, "..") || !strings.HasPrefix(k, "node_modules/") {
			return nil, fmt.Errorf("%s: bad package path %q", lockPath, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	nm := &NodeModules{Versions: map[string]string{}}
	var fetches []*Pkg
	for _, k := range keys {
		v := lf.Packages[k]
		name := k[strings.LastIndex(k, "node_modules/")+len("node_modules/"):]
		if strings.Count(k, "node_modules/") == 1 {
			nm.Versions[name] = v.Version
		}
		url, sri := v.Resolved, v.Integrity
		fl := "//third_party/npm:" + storeName(name+"@"+v.Version)
		fetches = append(fetches, b.add(&Pkg{Label: fl, Kind: "fetch", build: func(c *Ctx) (*drv.Derivation, error) {
			return c.fetch(storeName("npm-"+name+"-"+v.Version+".tgz"), url, sri)
		}}))
	}
	var p *Pkg
	p = b.add(&Pkg{Label: label, Kind: "node_modules", Deps: fetches, Tools: []string{"gnutar", "gzip"},
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			for i, k := range keys {
				fmt.Fprintf(&s, "mkdir -p $out/%s && tar -xzf %s --strip-components=1 --no-same-owner --no-same-permissions -C $out/%s\n", k, e.dep(fetches[i]), k)
			}
			s.WriteString("chmod -R u+rwX,go+rX $out\n")
			return e.finish(sanitize(label), s.String())
		}})
	nm.Pkg = p
	return nm, nil
}

type TSProject struct {
	b    *B
	Dir  string
	NM   *NodeModules
	gens []*Pkg
}

func (b *B) TSProject(dir string, nm *NodeModules) *TSProject {
	return &TSProject{b: b, Dir: dir, NM: nm}
}

// ConnectTS generates protobuf-es v2 code (messages + service descriptors used by connect-es)
// for lib and its imports into <dir>/gen/proto/<lib dir>.
func (t *TSProject) ConnectTS(lib *ProtoLibrary) *Pkg {
	key := "connect-ts:" + t.Dir + ":" + lib.Label
	if p, ok := t.b.memo[key]; ok {
		return p.(*Pkg)
	}
	var deps []*Pkg
	for _, d := range lib.Deps {
		deps = append(deps, t.ConnectTS(d))
	}
	plugins := "  - local: protoc-gen-es\n    out: gen\n    opt: [target=ts, import_extension=js]\n"
	p := t.b.protoGen(lib.Label+":ts", t.Dir+"/gen/proto/"+lib.Dir, lib, []string{"protoc-gen-es"}, plugins, deps)
	p.Checks = append(p.Checks, VersionCheck{Tool: "protoc-gen-es", ToolAttr: "protoc-gen-es", Runtime: "@bufbuild/protobuf", RuntimeVer: t.NM.Versions["@bufbuild/protobuf"], Rule: "equal", Where: t.Dir + "/package-lock.json"})
	t.gens = append(t.gens, p)
	t.b.memo[key] = p
	return p
}

func (t *TSProject) src() SrcSpec {
	return SrcSpec{Key: "ts:" + t.Dir, Dir: t.Dir, Include: func(rel string, isDir bool) bool {
		top := strings.Split(rel, "/")[0]
		return top != "gen" && top != "node_modules" && top != "dist" && top != ".bob"
	}}
}

// Build compiles the project with tsc (nixpkgs typescript) against node_modules and all codegen.
func (t *TSProject) Build(name string) *Pkg {
	label := "//" + t.Dir + ":" + name
	var p *Pkg
	p = t.b.add(&Pkg{Label: label, Kind: "ts_build", Tools: []string{"typescript", "nodejs"}, Srcs: []SrcSpec{t.src()},
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			fmt.Fprintf(&s, "mkdir -p $TMPDIR/w && cp -r %s/. $TMPDIR/w/ && chmod -R u+w $TMPDIR/w && cd $TMPDIR/w\n", e.src(p.Srcs[0].Key))
			for _, g := range t.gens {
				rel := strings.TrimPrefix(g.Out, t.Dir+"/")
				fmt.Fprintf(&s, "mkdir -p %s && cp -r %s/. %s/\n", rel, e.dep(g), rel)
			}
			nm := e.dep(t.NM.Pkg)
			fmt.Fprintf(&s, "ln -s %s/node_modules node_modules\ntsc -p .\n", nm)
			fmt.Fprintf(&s, "mkdir -p $out && cp -r dist $out/ && cp package.json $out/ && ln -s %s/node_modules $out/node_modules\n", nm)
			return e.finish(sanitize(label), s.String())
		}})
	p.Deps = append([]*Pkg{t.NM.Pkg}, t.gens...)
	return p
}

// ---------------------------------------------------------------- GraphQL

func fileSrc(key, dir string, files ...string) SrcSpec {
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	return SrcSpec{Key: key, Dir: dir, Include: func(rel string, isDir bool) bool {
		if isDir {
			for f := range set {
				if strings.HasPrefix(f, rel+"/") {
					return true
				}
			}
			return false
		}
		return set[rel]
	}}
}

// Gqlgen generates the gqlgen executable schema + models into <module>/gen/graph. Resolvers
// stay hand-written: bob never generates resolver stubs (they would be user-owned files).
func (m *GoModule) Gqlgen(schemaDir string, schemas ...string) *Pkg {
	label := "//" + schemaDir + ":gqlgen_go"
	out := m.Dir + "/gen/graph"
	var p *Pkg
	p = m.b.add(&Pkg{Label: label, Kind: "codegen", Out: out, Tools: []string{"gqlgen", "go"},
		Srcs: []SrcSpec{fileSrc("graphql-schema:"+schemaDir, schemaDir, schemas...), m.goModOnly()},
		Deps: []*Pkg{m.proxy},
		Checks: []VersionCheck{{Tool: "gqlgen", ToolAttr: "gqlgen", Runtime: "github.com/99designs/gqlgen",
			RuntimeVer: m.Requires["github.com/99designs/gqlgen"], Rule: "equal", Where: m.Dir + "/go.mod"}},
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			fmt.Fprintf(&s, "mkdir -p $TMPDIR/w/%s && cd $TMPDIR/w\ncp %s/go.mod %s/go.sum .\ncp -r %s/. %s/\nchmod -R u+w .\n",
				schemaDir, e.src(p.Srcs[1].Key), e.src(p.Srcs[1].Key), e.src(p.Srcs[0].Key), schemaDir)
			var sl []string
			for _, f := range schemas {
				sl = append(sl, schemaDir+"/"+f)
			}
			cfg := fmt.Sprintf("schema: [%s]\nexec:\n  filename: gen/graph/generated.go\n  package: graph\nmodel:\n  filename: gen/graph/model/models_gen.go\n  package: model\nskip_validation: true\nskip_mod_tidy: true\n", strings.Join(sl, ", "))
			s.WriteString(e.file("gqlgen.yml", "gqlgenYml", cfg))
			s.WriteString(m.goEnv(e))
			s.WriteString("gqlgen generate --config gqlgen.yml\nmkdir -p $out && cp -r gen/graph/. $out/\n")
			return e.finish(sanitize(label), s.String())
		}})
	m.gens = append(m.gens, p)
	return p
}

// GraphQLTS generates TypeScript types for the schema and the operations in docs, by calling
// graphql-codegen's plugin API through a bob-owned driver (no CLI, no codegen.yml).
func (t *TSProject) GraphQLTS(tool *NodeModules, driver, schemaDir string, schemas, docs []string) *Pkg {
	label := "//" + schemaDir + ":graphql_ts"
	out := t.Dir + "/gen/graphql"
	var p *Pkg
	p = t.b.add(&Pkg{Label: label, Kind: "codegen", Out: out, Tools: []string{"nodejs"},
		Srcs: []SrcSpec{fileSrc("graphql-docs:"+schemaDir, schemaDir, append(append([]string{}, schemas...), docs...)...),
			fileSrc("graphql-codegen-driver", filepath.Dir(driver), filepath.Base(driver))},
		Deps: []*Pkg{tool.Pkg},
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			fmt.Fprintf(&s, "mkdir -p $TMPDIR/w/%s && cd $TMPDIR/w\ncp -r %s/. %s/\ncp %s/%s driver.mjs\nln -s %s/node_modules node_modules\n",
				schemaDir, e.src(p.Srcs[0].Key), schemaDir, e.src(p.Srcs[1].Key), filepath.Base(driver), e.dep(tool.Pkg))
			var sl, dl []string
			for _, f := range schemas {
				sl = append(sl, schemaDir+"/"+f)
			}
			for _, f := range docs {
				dl = append(dl, schemaDir+"/"+f)
			}
			fmt.Fprintf(&s, "mkdir -p $out && node driver.mjs $out/graphql.ts %s -- %s\n", strings.Join(sl, " "), strings.Join(dl, " "))
			return e.finish(sanitize(label), s.String())
		}})
	t.gens = append(t.gens, p)
	return p
}

// ---------------------------------------------------------------- OpenAPI

// OpenAPIGo generates models + a net/http (Go 1.22 ServeMux) server interface into <module>/gen/<pkg>.
func (m *GoModule) OpenAPIGo(specDir, spec, pkg string) *Pkg {
	label := "//" + specDir + ":openapi_go"
	out := m.Dir + "/gen/" + pkg
	var p *Pkg
	p = m.b.add(&Pkg{Label: label, Kind: "codegen", Out: out, Tools: []string{"oapi-codegen"},
		Srcs: []SrcSpec{fileSrc("openapi:"+specDir+"/"+spec, specDir, spec), m.goModOnly()},
		Checks: []VersionCheck{{Tool: "oapi-codegen", ToolAttr: "oapi-codegen", Runtime: "github.com/oapi-codegen/runtime",
			RuntimeVer: m.Requires["github.com/oapi-codegen/runtime"], Rule: "present", Where: m.Dir + "/go.mod"}},
		build: func(c *Ctx) (*drv.Derivation, error) {
			e, err := c.env(p.Tools)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			fmt.Fprintf(&s, "mkdir -p $TMPDIR/w/%s && cd $TMPDIR/w\ncp %s/go.mod .\ncp %s/%s %s/\n", specDir, e.src(p.Srcs[1].Key), e.src(p.Srcs[0].Key), spec, specDir)
			cfg := fmt.Sprintf("package: %s\ngenerate:\n  std-http-server: true\n  models: true\noutput: out/%s.gen.go\n", pkg, strings.TrimSuffix(path.Base(spec), path.Ext(spec)))
			s.WriteString(e.file("oapi.yaml", "oapiYaml", cfg))
			fmt.Fprintf(&s, "export HOME=$TMPDIR\nmkdir -p out && oapi-codegen -config oapi.yaml %s/%s\nmkdir -p $out && cp out/* $out/\n", specDir, spec)
			return e.finish(sanitize(label), s.String())
		}})
	m.gens = append(m.gens, p)
	return p
}
