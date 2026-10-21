// Package ocamlgen computes derivations for opam packages and bob's own OCaml code.
package ocamlgen

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bobspike/s05/drv"
	"bobspike/s05/nixidx"
	"bobspike/s05/opam"
)

// Toolchain from nixpkgs. No stdenv: bash scripts call the tools directly (backend B style).
var Toolchain = []string{"ocamlPackages.ocaml", "ocamlPackages.findlib", "ocamlPackages.dune_3", "opam-installer",
	"stdenv.cc", "stdenv.cc.bintools", "coreutils", "bash", "gnutar", "gzip", "bzip2:bin", "xz:bin", "gnumake",
	"gnused", "gnugrep", "gawk", "findutils", "diffutils", "which", "gnupatch", "pkg-config"}

// ConfMap maps opam conf-* packages to nixpkgs packages (curated, like R-NPM-005's quirks table).
var ConfMap = map[string][]string{
	"conf-pkg-config": {"pkg-config"}, "conf-gmp": {"gmp.dev", "gmp"}, "conf-libev": {"libev"},
	"conf-libssl": {"openssl.dev", "openssl.out"}, "conf-gmp-powm-sec": {"gmp.dev", "gmp"},
}

// Locked is one opam package in bob.lock.
type Locked struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	URL      string   `json:"url,omitempty"`
	Hash     string   `json:"hash,omitempty"` // SRI, from the opam file's checksum (sha512 or sha256)
	OpamHash string   `json:"opam"`           // sha256 of the opam file in the pinned repository
	Deps     []string `json:"deps,omitempty"` // build-order deps (depends + present depopts)
}

type Lock struct {
	Repo     string             `json:"opamRepository"`
	Packages map[string]*Locked `json:"packages"`
}

type Gen struct {
	Ix     *nixidx.Index
	System string
	Repo   string // opam repository checkout
	Lock   *Lock
	Srcs   map[string]string // local name -> imported store path

	Order  []*drv.Derivation
	Labels map[string]string
	pkgs   map[string]*drv.Derivation
	Stats  map[string]int
}

func New(ix *nixidx.Index, repo string, l *Lock, srcs map[string]string) *Gen {
	return &Gen{Ix: ix, System: "aarch64-linux", Repo: repo, Lock: l, Srcs: srcs,
		Labels: map[string]string{}, pkgs: map[string]*drv.Derivation{}, Stats: map[string]int{}}
}

func (g *Gen) attr(a, out string) (*nixidx.Attr, string, error) {
	at := g.Ix.Attrs[a]
	if at == nil || at.DrvPath == "" {
		return nil, "", fmt.Errorf("nixpkgs attribute %s not resolved", a)
	}
	if out == "" {
		out = "out"
	}
	return at, at.Outputs[out], nil
}

type env struct {
	inputs map[string][]string
	srcs   []string
	path   []string
}

func (g *Gen) toolEnv(extra ...string) (*env, error) {
	e := &env{inputs: map[string][]string{}}
	for _, a := range append(append([]string{}, Toolchain...), extra...) {
		name, out, _ := strings.Cut(a, ":")
		at, p, err := g.attr(name, out)
		if err != nil {
			return nil, err
		}
		if out == "" {
			out = "out"
		}
		e.inputs[at.DrvPath] = append(e.inputs[at.DrvPath], out)
		e.path = append(e.path, p+"/bin")
	}
	return e, nil
}

func (g *Gen) Tool(a string) string { _, p, _ := g.attr(a, ""); return p }

func (g *Gen) finish(kind, name, label string, e *env, script string, extraEnv map[string]string) (*drv.Derivation, error) {
	_, bash, err := g.attr("bash", "")
	if err != nil {
		return nil, err
	}
	envm := map[string]string{"name": name, "system": g.System, "builder": bash + "/bin/bash",
		"PATH": strings.Join(e.path, ":")}
	for k, v := range extraEnv {
		envm[k] = v
	}
	d := &drv.Derivation{Name: name, System: g.System, Builder: bash + "/bin/bash",
		Args: []string{"-c", prelude + script}, Env: envm, InputDrvs: e.inputs, InputSrcs: e.srcs}
	for k, v := range d.InputDrvs {
		sort.Strings(v)
		d.InputDrvs[k] = uniq(v)
	}
	sort.Strings(d.InputSrcs)
	d.InputSrcs = uniq(d.InputSrcs)
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return nil, err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	g.Labels[d.DrvPath] = label
	g.Stats[kind]++
	return d, nil
}

const prelude = `set -eu -o pipefail
trap 'echo "bob-ml: command failed (exit $?): $BASH_COMMAND" >&2' ERR
export HOME=$TMPDIR
mkdir -p $out
`

// fetch is builtin:fetchurl, flat hash taken from the opam file's checksum.
func (g *Gen) fetch(l *Locked) (*drv.Derivation, error) {
	algo, b64, ok := strings.Cut(l.Hash, "-")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil {
		return nil, fmt.Errorf("opam:%s.%s: bob.lock has no valid source hash", l.Name, l.Version)
	}
	name := sanitize(filepath.Base(strings.SplitN(l.URL, "?", 2)[0]))
	d := &drv.Derivation{Name: name, System: "builtin", Builder: "builtin:fetchurl",
		Env: map[string]string{"name": name, "system": "builtin", "builder": "builtin:fetchurl",
			"url": l.URL, "urls": l.URL, "outputHash": l.Hash, "outputHashAlgo": "",
			"outputHashMode": "flat", "preferLocalBuild": "1", "executable": "", "unpack": ""},
		FixedAlgo: algo, FixedMode: "flat", FixedHash: raw}
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return nil, err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	g.Labels[d.DrvPath] = "opam:" + l.Name + "." + l.Version + " (download)"
	g.Stats["fetch"]++
	return d, nil
}

// Closure returns name and its transitive opam deps (sorted, name first excluded).
func (g *Gen) Closure(names []string) ([]string, error) {
	seen := map[string]bool{}
	var walk func(n string) error
	walk = func(n string) error {
		if seen[n] {
			return nil
		}
		l := g.Lock.Packages[n]
		if l == nil {
			return fmt.Errorf("opam package %s is not in bob.lock (run bob lock)", n)
		}
		seen[n] = true
		for _, d := range l.Deps {
			if err := walk(d); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range names {
		if err := walk(n); err != nil {
			return nil, err
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Package returns the derivation building one opam package with its own build instructions.
func (g *Gen) Package(name string) (*drv.Derivation, error) {
	if d := g.pkgs[name]; d != nil {
		return d, nil
	}
	l := g.Lock.Packages[name]
	if l == nil {
		return nil, fmt.Errorf("opam package %s is not in bob.lock (run bob lock)", name)
	}
	label := "opam:" + l.Name + "." + l.Version
	dir := filepath.Join(g.Repo, "packages", l.Name, l.Name+"."+l.Version)
	b, err := os.ReadFile(filepath.Join(dir, "opam"))
	if err != nil {
		return nil, fmt.Errorf("%s: opam file missing from the pinned repository", label)
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != l.OpamHash {
		return nil, fmt.Errorf("%s: opam file does not match bob.lock", label)
	}
	f, err := opam.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", label, err)
	}
	if !strings.HasPrefix(name, "conf-") && (f.Fields["patches"] != nil || len(f.Sections) > 1) {
		return nil, fmt.Errorf("%s: patches/extra-source are not supported by the spike", label)
	}
	clo, err := g.Closure(l.Deps)
	if err != nil {
		return nil, err
	}
	// conf-* packages are system checks: bob maps them to nixpkgs packages and exposes those
	// to every package that has the conf package in its closure.
	var extra []string
	isConf := strings.HasPrefix(name, "conf-")
	for _, n := range append([]string{name}, clo...) {
		if m, ok := ConfMap[n]; ok {
			extra = append(extra, m...)
		} else if strings.HasPrefix(n, "conf-") && n == name {
			return nil, fmt.Errorf("%s: no nixpkgs mapping for this system package (add it to bob's conf table)", label)
		}
	}
	var toolX []string
	for _, a := range extra {
		n, o, _ := strings.Cut(a, ".")
		if o == "" {
			o = "out"
		}
		toolX = append(toolX, n+":"+o)
	}
	e, err := g.toolEnv(toolX...)
	if err != nil {
		return nil, err
	}
	deps := map[string]string{}
	var libs, stub []string
	for _, dn := range clo {
		dd, err := g.Package(dn)
		if err != nil {
			return nil, err
		}
		e.inputs[dd.DrvPath] = []string{"out"}
		deps[dn] = dd.OutPath
		libs = append(libs, dd.OutPath+"/lib")
		stub = append(stub, dd.OutPath+"/lib/stublibs")
		e.path = append(e.path, dd.OutPath+"/bin")
	}
	findlib := g.Tool("ocamlPackages.findlib")
	ocaml := g.Tool("ocamlPackages.ocaml")
	var s strings.Builder
	s.WriteString("cd $TMPDIR\n")
	if l.URL != "" {
		fd, err := g.fetch(l)
		if err != nil {
			return nil, err
		}
		e.inputs[fd.DrvPath] = []string{"out"}
		fmt.Fprintf(&s, "mkdir src && cd src && tar xf %s --strip-components=1 --no-same-owner --no-same-permissions\n", fd.OutPath)
	}
	libs = append(libs, fmt.Sprintf("%s/lib/ocaml/%s/site-lib", findlib, g.Ix.Attrs["ocamlPackages.ocaml"].Version))
	fmt.Fprintf(&s, "export OCAMLPATH=%s\n", strings.Join(libs, ":"))
	fmt.Fprintf(&s, "export OCAMLFIND_DESTDIR=$out/lib OCAMLFIND_LDCONF=ignore\n")
	fmt.Fprintf(&s, "export CAML_LD_LIBRARY_PATH=%s\n", strings.Join(append(stub, "$out/lib/stublibs", ocaml+"/lib/ocaml/stublibs"), ":"))
	fmt.Fprintf(&s, "export OCAMLTOP_INCLUDE_PATH=%s/lib/ocaml/%s/site-lib\n", findlib, g.Ix.Attrs["ocamlPackages.ocaml"].Version)
	if len(extra) > 0 {
		var pc, inc, lib []string
		for _, a := range extra {
			n, o, _ := strings.Cut(a, ".")
			_, p, _ := g.attr(n, o)
			pc = append(pc, p+"/lib/pkgconfig", p+"/share/pkgconfig")
			inc = append(inc, p+"/include")
			lib = append(lib, p+"/lib")
		}
		fmt.Fprintf(&s, "export PKG_CONFIG_PATH=%s C_INCLUDE_PATH=%s LIBRARY_PATH=%s\n", strings.Join(pc, ":"), strings.Join(inc, ":"), strings.Join(lib, ":"))
	}
	s.WriteString("mkdir -p $out/lib $out/bin $out/lib/stublibs\n")
	venv := g.varEnv(l, f, deps)
	fields := []string{"build", "install"}
	if isConf {
		fields = nil
	}
	for _, field := range fields {
		cmds, err := commands(f.Fields[field], venv)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %v", label, field, err)
		}
		for _, c := range cmds {
			s.WriteString(c + "\n")
		}
	}
	fmt.Fprintf(&s, "if [ -f %[1]s.install ]; then opam-installer --prefix=$out --libdir=$out/lib --stubsdir=$out/lib/stublibs %[1]s.install; fi\n", shq(l.Name))
	d, err := g.finish("opam", sanitize("opam-"+l.Name+"-"+l.Version), label, e, s.String(), nil)
	if err != nil {
		return nil, err
	}
	g.pkgs[name] = d
	return d, nil
}

const outM, jobsM = "\x00OUT\x00", "\x00JOBS\x00"

func (g *Gen) varEnv(l *Locked, f *opam.File, deps map[string]string) opam.Env {
	plat := opam.DefaultPlatform(g.Ix.Attrs["ocamlPackages.ocaml"].Version)
	plat["jobs"] = jobsM
	return func(n string) (string, bool) {
		pk, v, scoped := strings.Cut(n, ":")
		if !scoped {
			v, pk = n, ""
		}
		self := pk == "_" || pk == l.Name
		if pk == "" || self {
			switch v {
			case "name":
				return l.Name, true
			case "version":
				return l.Version, true
			case "prefix":
				return outM, true
			case "bin", "sbin":
				return outM + "/bin", true
			case "lib", "share", "doc", "etc":
				if self {
					return outM + "/" + v + "/" + l.Name, true
				}
				return outM + "/" + v, true
			case "man":
				return outM + "/man", true
			case "stublibs":
				return outM + "/lib/stublibs", true
			case "toplevel":
				return outM + "/lib/toplevel", true
			case "installed":
				return "true", true
			}
			if x, ok := plat[n]; ok {
				return x, true
			}
			return "", false
		}
		if x, ok := plat[n]; ok {
			return x, true
		}
		p, ok := deps[pk]
		_, provided := map[string]bool{"ocaml": true, "dune": true, "ocamlfind": true, "base-unix": true, "base-threads": true, "base-bigarray": true, "base-domains": true}[pk]
		switch v {
		case "installed":
			return fmt.Sprint(ok || provided), true
		case "lib", "share", "doc":
			if ok {
				return p + "/" + v + "/" + pk, true
			}
		case "bin":
			if ok {
				return p + "/bin", true
			}
		case "version":
			if dl := g.Lock.Packages[pk]; dl != nil {
				return dl.Version, true
			}
		}
		return "", false
	}
}

// commands renders an opam build/install field into shell lines.
func commands(v *opam.V, env opam.Env) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	var list []*opam.V
	if v.K == opam.List && len(v.L) > 0 && v.L[0].K == opam.List {
		list = v.L
	} else if v.K == opam.List && len(v.L) > 0 {
		list = []*opam.V{v}
	}
	var out []string
	for _, c := range list {
		if !filterOK(c.Opt, env) {
			continue
		}
		var args []string
		for _, a := range c.L {
			if !filterOK(a.Opt, env) {
				continue
			}
			var s string
			switch a.K {
			case opam.Str:
				s = opam.Interp(a.S, env)
			case opam.Ident:
				x, ok := env(a.S)
				if !ok {
					return nil, fmt.Errorf("undefined variable %s", a.S)
				}
				s = x
			default:
				return nil, fmt.Errorf("line %d: unsupported argument", a.Line)
			}
			args = append(args, shq(s))
		}
		if len(args) > 0 {
			line := strings.Join(args, " ")
			line = strings.ReplaceAll(line, outM, `'"$out"'`)
			line = strings.ReplaceAll(line, jobsM, `'"$NIX_BUILD_CORES"'`)
			out = append(out, line)
		}
	}
	return out, nil
}

func filterOK(opts []*opam.V, env opam.Env) bool {
	for _, o := range opts {
		if b, ok := opam.Eval(o, env).Bool(); !ok || !b {
			return false
		}
	}
	return true
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func sanitize(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+-._?=", c) {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
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
