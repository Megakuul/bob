package gen

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// lockSection decodes the cc section of the request (an empty one when bob.lock has none).
func lockSection(raw json.RawMessage) (*lock.CCSection, error) {
	if len(raw) == 0 {
		return &lock.CCSection{Externals: map[string]lock.CExternal{}}, nil
	}
	v, err := lock.DecodeSection(lock.SectionCC, raw)
	if err != nil {
		return nil, err
	}
	sec := v.(*lock.CCSection)
	if sec.Externals == nil {
		sec.Externals = map[string]lock.CExternal{}
	}
	return sec, nil
}

// sriHex turns "sha256-<base64>" into lowercase hex.
func sriHex(sri string) (string, error) {
	b64, ok := strings.CutPrefix(sri, "sha256-")
	if !ok {
		return "", fmt.Errorf("hash %q is not a sha256 SRI hash", sri)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("hash %q is not a sha256 SRI hash", sri)
	}
	return hex.EncodeToString(raw), nil
}

// sourceName is the store name of an External's pinned source (§03.1.3).
func sourceName(name, hexHash string) string { return drv.Name("ccx-src", name, hexHash[:8]) }

// planExternal plans an External: its pinned source (fetched by bob, R-CC-005a) and one
// cc-external derivation running the project's own build system.
func (pl *planner) planExternal(n *ir.Node) error {
	var x ccspec.External
	if err := json.Unmarshal(n.Spec, &x); err != nil {
		return internal(n, "spec: %v", err)
	}
	c, err := configOf(n)
	if err != nil {
		return internal(n, "%v", err)
	}
	sec, err := lockSection(pl.req.Lock)
	if err != nil {
		return internal(n, "bob.lock cc section: %v", err)
	}
	e, ok := sec.Externals[n.Label]
	if !ok || e.URL != x.URL || e.Strip != x.Strip {
		d := diag.New(1009, "label", n.Label)
		d.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
		return d
	}
	h, err := sriHex(e.NarHash)
	if err != nil {
		return internal(n, "bob.lock: %v", err)
	}
	name := sourceName(x.Name, h)
	src, err := drv.FixedOutputPath("sha256", h, true, name)
	if err != nil {
		return internal(n, "%v", err)
	}
	pl.out.Fetches = append(pl.out.Fetches, fetch.Request{URLs: []string{e.URL}, Kind: fetch.Archive,
		NarHash: e.NarHash, Name: name, Strip: e.Strip, Label: n.Label})

	in := newInputs(pl.req.Builder)
	in.srcs[src] = true
	ts, err := pl.tools(n, x.CC, c, in)
	if err != nil {
		return err
	}
	path, shell := ts.path, ""
	for _, ti := range x.Tools {
		tn, rv, err := pl.dep(n, ti)
		if err != nil {
			return err
		}
		p := in.add(rv, pickOutput(rv.Outputs, "bin", "out"))
		path = append(path, p+"/bin")
		if tn != nil && tn.Nix != nil && tn.Nix.Attr == "bash" {
			shell = p + "/bin/bash"
		}
	}
	if shell == "" {
		return internal(n, "no bash among the External's tools")
	}
	cflags := append(c.profileFlags(), "-ffile-prefix-map="+buildTop+"=.")
	var ldflags, pcPath, prefixes []string
	lf := pl.newLibFlags(in)
	ids, err := depIDs(n, x.Deps)
	if err != nil {
		return err
	}
	for _, id := range ids {
		l, err := pl.libOf(id)
		if err != nil {
			return err
		}
		outs, _, err := lf.outputs(l)
		if err != nil {
			return err
		}
		for _, d := range staticIncludes(l, pl.req.Inputs[id]) {
			cflags = append(cflags, "-I"+d)
		}
		for _, o := range []string{"dev", "out", "lib"} {
			if p, ok := outs[o]; ok {
				pcPath = append(pcPath, p+"/lib/pkgconfig", p+"/share/pkgconfig")
				prefixes = append(prefixes, p)
			}
		}
		if lib := outs[pickOutput(outs, "lib", "out")]; lib != "" {
			ldflags = append(ldflags, "-L"+lib+"/lib", "-Wl,-rpath,"+lib+"/lib")
		}
	}
	env := map[string]string{"CC": c.cmd("gcc"), "CXX": c.cmd("g++"), "CFLAGS": strings.Join(cflags, " "),
		"CXXFLAGS": strings.Join(cflags, " "), "LDFLAGS": strings.Join(ldflags, " "),
		"PKG_CONFIG_PATH": strings.Join(pcPath, ":"), "CMAKE_PREFIX_PATH": strings.Join(prefixes, ":")}
	cargs, err := crossArgs(n, x, c, env)
	if err != nil {
		return err
	}
	out := drv.Placeholder("out")
	args := map[string]any{"src": src, "build": x.Build, "args": cargs, "profile": c.profile, "path": path,
		"shell": shell, "env": env, "out": out}
	fx := &fixup{Strip: c.profile != string(bob.Debug), ShrinkRPath: true,
		Forbidden: append(append([]string{}, ts.forbids...), src, pl.req.Builder), Tools: ts.fixup}
	p, d, err := pl.job(c.name("ccx", x.Name), c.system, "cc-external", n.Label, args, fx, in)
	if err != nil {
		return internal(n, "%v", err)
	}
	o := outPath(d)
	li := &bob.LinkInfo{IncludeDirs: []string{o + "/" + x.Include}, LinkDirs: []string{o + "/lib"},
		Libs: append([]string{}, x.Libs...), RuntimeDirs: []string{o + "/lib"}, PkgConfig: []string{o + "/lib/pkgconfig"}}
	pl.out.Nodes[n.ID] = engine.Resolved{Drv: p, Outputs: map[string]string{"out": o}, LinkInfo: li}
	return nil
}

// crossArgs returns the External's configure arguments, preceded for a cross target by what its
// build system needs to cross-compile (R-PLAT-005); env gets the prefixed binutils.
func crossArgs(n *ir.Node, x ccspec.External, c config, env map[string]string) ([]string, error) {
	if !c.tc.Cross() {
		return x.Args, nil
	}
	for _, t := range []string{"ar", "ranlib", "strip", "nm", "ld", "objcopy"} {
		env[strings.ToUpper(t)] = c.cmd(t)
	}
	var pre []string
	switch x.Build {
	case ccspec.BuildCMake:
		cpu, _, _ := strings.Cut(c.tc.Triple, "-")
		pre = []string{"-DCMAKE_SYSTEM_NAME=Linux", "-DCMAKE_SYSTEM_PROCESSOR=" + cpu,
			"-DCMAKE_C_COMPILER=" + c.cmd("gcc"), "-DCMAKE_CXX_COMPILER=" + c.cmd("g++")}
	case ccspec.BuildAutotools:
		pre = []string{"--host=" + c.tc.Triple}
	case ccspec.BuildMeson:
		// Meson cross-compiles only with a cross file, which the cc-external builder cannot write yet.
		return nil, diag.NotImplemented("cross-compiling a Meson cc.External (" + n.Label + ")")
	}
	return append(pre, x.Args...), nil
}
