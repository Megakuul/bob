package bop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
)

// Kinds returns the Rust job kinds (SPEC §03.0.4); cmd/bob-builder adds them to builder.Core().
// rustc writes deterministic, stripped outputs (-C strip=debuginfo, remapped paths), so the
// fixup is skipped and rlib bytes stay exactly what the compiler wrote.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{
		"rustc":          {Run: rustcKind, NoFixup: true},
		"rust-build-run": {Run: buildRunKind, NoFixup: true},
		"rust-std":       {Run: rustStdKind, NoFixup: true},
	}
}

// maxScriptOutput caps the stdout of a build script bob keeps and parses.
const maxScriptOutput = 64 << 20

func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return builder.Invalidf("rust job args: %v", err)
	}
	return nil
}

// env is the environment of the toolchain's programs: the R-NIX-016 base plus extra.
func env(c *builder.Ctx, t Toolchain, tmp string, extra map[string]string) []string {
	e := map[string]string{
		"PATH": strings.Join(t.Path, ":"), "HOME": tmp, "TMPDIR": tmp, "SOURCE_DATE_EPOCH": "1",
		"TZ": "UTC", "LC_ALL": "C", "TERM": "dumb", "NIX_BUILD_CORES": strconv.Itoa(cores()),
	}
	for k, v := range c.Outputs {
		e[k] = v
	}
	for k, v := range extra {
		e[k] = v
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + e[k]
	}
	return out
}

func cores() int {
	n, err := strconv.Atoi(os.Getenv("NIX_BUILD_CORES"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func out(c *builder.Ctx) (string, error) {
	o := c.Outputs["out"]
	if o == "" {
		return "", builder.Invalidf("rust job: no output \"out\"")
	}
	return o, nil
}

func readDirectives(run string) (*Directives, error) {
	b, err := os.ReadFile(filepath.Join(run, "directives.json"))
	if err != nil {
		return nil, fmt.Errorf("build-script output %s: %w", run, err)
	}
	var d Directives
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("build-script output %s: %w", run, err)
	}
	return &d, nil
}

// linking reports whether rustc links a final artifact for the crate type.
func linking(a *RustcArgs) bool {
	return a.Test || a.CrateType == "bin" || a.CrateType == "cdylib" || a.CrateType == "staticlib" || a.CrateType == "proc-macro"
}

// rustcArgv builds the rustc command line and the extra environment of a compile.
func rustcArgv(a *RustcArgs, o string) ([]string, map[string]string, error) {
	dir := filepath.Join(o, "lib")
	if a.Test || a.CrateType == "bin" {
		dir = filepath.Join(o, "bin")
	}
	argv := []string{a.Rustc, "--crate-name", a.CrateName, "--edition", a.Edition}
	if a.Test {
		argv = append(argv, "--test")
	} else {
		argv = append(argv, "--crate-type", a.CrateType)
	}
	argv = append(argv, filepath.Join(a.Src, a.Root), "--emit=link", "-C", "opt-level="+a.OptLevel,
		"-C", "metadata="+a.Metadata, "-C", "strip=debuginfo", "--remap-path-prefix", a.Src+"="+a.Remap)
	if a.OutName != "" {
		argv = append(argv, "-o", filepath.Join(dir, a.OutName))
	} else {
		argv = append(argv, "--out-dir", dir)
	}
	if !a.Test && (a.CrateType == "lib" || a.CrateType == "proc-macro") {
		argv = append(argv, "-C", "extra-filename=-"+a.Metadata, "-C", "embed-bitcode=no")
	}
	if a.CrateType == "proc-macro" && !a.Test {
		argv = append(argv, "--extern", "proc_macro")
	}
	if a.Target != "" {
		argv = append(argv, "--target", a.Target)
	}
	if a.Sysroot != "" {
		argv = append(argv, "--sysroot", a.Sysroot)
	}
	if a.Linker != "" {
		argv = append(argv, "-C", "linker="+a.Linker)
	}
	if a.CapLints {
		argv = append(argv, "--cap-lints", "allow")
	}
	for _, f := range a.Features {
		argv = append(argv, "--cfg", "feature="+strconv.Quote(f))
	}
	if a.CheckCfg {
		vals := make([]string, len(a.FeatureSet))
		for i, f := range a.FeatureSet {
			vals[i] = strconv.Quote(f)
		}
		argv = append(argv, "--check-cfg", "cfg(docsrs,test)", "--check-cfg", "cfg(feature, values("+strings.Join(vals, ", ")+"))")
	}
	for _, e := range a.Externs {
		argv = append(argv, "--extern", e.Name+"="+e.File)
	}
	for _, d := range a.DepDirs {
		argv = append(argv, "-L", "dependency="+d)
	}
	extra := map[string]string{"CARGO_MANIFEST_DIR": a.Src, "CARGO_MANIFEST_PATH": filepath.Join(a.Src, "Cargo.toml"),
		"CARGO_CRATE_NAME": a.CrateName}
	for k, v := range a.Env {
		extra[k] = v
	}
	if a.BuildRun != "" {
		d, err := readDirectives(a.BuildRun)
		if err != nil {
			return nil, nil, err
		}
		od := filepath.Join(a.BuildRun, "out-dir")
		extra["OUT_DIR"] = od
		argv = append(argv, "--remap-path-prefix", od+"="+a.Remap+"/out")
		for _, c := range d.Cfgs {
			argv = append(argv, "--cfg", c)
		}
		if a.CheckCfg {
			for _, c := range d.CheckCfgs {
				argv = append(argv, "--check-cfg", c)
			}
		}
		for k, v := range d.Env {
			extra[k] = v
		}
		for _, l := range d.LinkLibs {
			argv = append(argv, "-l", l)
		}
		for _, l := range d.LinkSearch {
			argv = append(argv, "-L", l)
		}
		if a.Test || a.CrateType == "bin" || a.CrateType == "cdylib" {
			for _, la := range d.LinkArgs {
				argv = append(argv, "-C", "link-arg="+la)
			}
		}
	}
	if linking(a) {
		seen := map[string]bool{}
		for _, run := range a.LinkFrom {
			d, err := readDirectives(run)
			if err != nil {
				return nil, nil, err
			}
			for _, l := range d.LinkSearch {
				if !seen[l] {
					seen[l] = true
					argv = append(argv, "-L", l)
				}
			}
		}
	}
	for _, s := range a.Search {
		argv = append(argv, "-L", s)
	}
	for _, l := range a.Libs {
		argv = append(argv, "-l", l)
	}
	for _, la := range a.LinkArgs {
		argv = append(argv, "-C", "link-arg="+la)
	}
	return argv, extra, nil
}

// rustcKind is kind rustc: one rustc invocation.
func rustcKind(c *builder.Ctx, raw json.RawMessage) error {
	var a RustcArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !filepath.IsAbs(a.Rustc) || !filepath.IsAbs(a.Src) || a.CrateName == "" || a.Root == "" || a.Metadata == "" {
		return builder.Invalidf("rustc job: rustc, src, crateName, root and metadata are required")
	}
	switch a.CrateType {
	case "lib", "proc-macro", "bin", "staticlib", "cdylib":
	default:
		return builder.Invalidf("rustc job: crate type %q", a.CrateType)
	}
	o, err := out(c)
	if err != nil {
		return err
	}
	argv, extra, err := rustcArgv(&a, o)
	if err != nil {
		return err
	}
	for _, d := range []string{"lib", "bin"} {
		if (d == "bin") == (a.Test || a.CrateType == "bin") {
			if err := os.MkdirAll(filepath.Join(o, d), 0o755); err != nil {
				return err
			}
		}
	}
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	c.Phase("rustc")
	cmd := &exec.Cmd{Path: a.Rustc, Args: argv, Env: env(c, a.Toolchain, tmp, extra), Dir: tmp, Stdout: os.Stderr, Stderr: os.Stderr}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("rustc failed for crate %s (%s)", a.CrateName, ee.ProcessState.String())
		}
		return fmt.Errorf("rustc: %w", err)
	}
	if a.Headers != "" {
		if err := copyTree(filepath.Join(a.Src, a.Headers), filepath.Join(o, "include")); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies the regular files and directories below from to to.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		t := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}

// buildRunKind is kind rust-build-run: run a build script the way Cargo does (S3 §3.3).
func buildRunKind(c *builder.Ctx, raw json.RawMessage) error {
	var a BuildRunArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !filepath.IsAbs(a.Rustc) || !filepath.IsAbs(a.Src) || !filepath.IsAbs(a.Script) {
		return builder.Invalidf("rust-build-run job: rustc, src and script are required")
	}
	o, err := out(c)
	if err != nil {
		return err
	}
	od := filepath.Join(o, "out-dir")
	if err := os.MkdirAll(od, 0o755); err != nil {
		return err
	}
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	extra := map[string]string{"OUT_DIR": od, "CARGO_MANIFEST_DIR": a.Src, "CARGO_MANIFEST_PATH": filepath.Join(a.Src, "Cargo.toml"),
		"RUSTC": a.Rustc, "RUSTDOC": filepath.Join(filepath.Dir(a.Rustc), "rustdoc"), "NUM_JOBS": strconv.Itoa(cores()),
		"CARGO_ENCODED_RUSTFLAGS": "", "CC": "cc", "CXX": "c++", "AR": "ar", "CARGO": "cargo-is-not-available"}
	for k, v := range a.Env {
		extra[k] = v
	}
	for _, dl := range a.DepLinks {
		d, err := readDirectives(dl.Run)
		if err != nil {
			return err
		}
		pre := "DEP_" + envName(dl.Links) + "_"
		for k, v := range d.Metadata {
			extra[pre+envName(k)] = v
		}
	}
	c.Phase("build-script")
	var stdout bytes.Buffer
	cmd := &exec.Cmd{Path: a.Script, Args: []string{a.Script}, Env: env(c, a.Toolchain, tmp, extra), Dir: a.Src,
		Stdout: &limited{w: &stdout, n: maxScriptOutput}, Stderr: os.Stderr}
	runErr := cmd.Run()
	if err := os.WriteFile(filepath.Join(o, "output"), stdout.Bytes(), 0o644); err != nil {
		return err
	}
	d, perr := ParseDirectives(stdout.Bytes())
	for _, w := range d.Warnings {
		fmt.Fprintln(os.Stderr, "warning (build script):", w)
	}
	for _, u := range d.Unsupported {
		fmt.Fprintln(os.Stderr, "note (build script): bob ignores the directive", u)
	}
	if runErr != nil {
		_, _ = os.Stderr.Write(tail(stdout.Bytes(), 40))
		name := a.Env["CARGO_PKG_NAME"]
		msg := "the build script of " + name + " failed"
		if strings.HasSuffix(name, "-sys") && a.Env["PKG_CONFIG_PATH"] == "" {
			msg += fmt.Sprintf("; hint: give it its native library with rust.Uses(%q, nixpkgs.Lib(p, \"<attr>\"))", name)
		}
		c.Diag("error", "", 0, 0, msg)
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return fmt.Errorf("build script failed (%s)", ee.ProcessState.String())
		}
		return fmt.Errorf("build script: %w", runErr)
	}
	if perr != nil {
		return perr
	}
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o, "directives.json"), append(b, '\n'), 0o644)
}

// limited writes at most n bytes to w and drops the rest.
type limited struct {
	w interface{ Write([]byte) (int, error) }
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	k := len(p)
	if k > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	if len(p) > 0 {
		if _, err := l.w.Write(p); err != nil {
			return 0, err
		}
	}
	return k, nil
}

func tail(b []byte, n int) []byte {
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return bytes.Join(lines, []byte("\n"))
}

func envName(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

// ParseDirectives implements the build-script output contract (Cargo reference, "Build
// Scripts" → "Outputs of the Build Script") for the old `cargo:` and the new `cargo::` syntax.
// `rerun-if-*` are ignored (a changed input rebuilds anyway); `error` fails the run.
func ParseDirectives(out []byte) (*Directives, error) {
	d := &Directives{Cfgs: []string{}, CheckCfgs: []string{}, Env: map[string]string{}, LinkLibs: []string{},
		LinkSearch: []string{}, LinkArgs: []string{}, Metadata: map[string]string{}, Warnings: []string{}, Unsupported: []string{}}
	var errs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		var rest string
		newSyntax := false
		switch {
		case strings.HasPrefix(line, "cargo::"):
			rest, newSyntax = line[len("cargo::"):], true
		case strings.HasPrefix(line, "cargo:"):
			rest = line[len("cargo:"):]
		default:
			continue
		}
		key, val, ok := strings.Cut(rest, "=")
		if !ok {
			continue
		}
		switch key {
		case "rustc-cfg":
			d.Cfgs = append(d.Cfgs, val)
		case "rustc-check-cfg":
			d.CheckCfgs = append(d.CheckCfgs, val)
		case "rustc-env":
			k, v, _ := strings.Cut(val, "=")
			d.Env[k] = v
		case "rustc-link-lib":
			d.LinkLibs = append(d.LinkLibs, val)
		case "rustc-link-search":
			d.LinkSearch = append(d.LinkSearch, val)
		case "rustc-flags":
			f := strings.Fields(val)
			for i := 0; i < len(f); i++ {
				flag, v := f[i], ""
				switch {
				case (flag == "-l" || flag == "-L") && i+1 < len(f):
					v = f[i+1]
					i++
				case strings.HasPrefix(flag, "-l") || strings.HasPrefix(flag, "-L"):
					flag, v = flag[:2], flag[2:]
				default:
					d.Unsupported = append(d.Unsupported, "rustc-flags "+flag)
					continue
				}
				if flag == "-l" {
					d.LinkLibs = append(d.LinkLibs, v)
				} else {
					d.LinkSearch = append(d.LinkSearch, v)
				}
			}
		case "rustc-link-arg", "rustc-link-arg-bins", "rustc-cdylib-link-arg", "rustc-link-arg-cdylib":
			d.LinkArgs = append(d.LinkArgs, val)
		case "rustc-link-arg-bin", "rustc-link-arg-tests", "rustc-link-arg-examples", "rustc-link-arg-benches":
			// per-target link args of targets bob does not build from registry crates
		case "warning":
			d.Warnings = append(d.Warnings, val)
		case "error":
			errs = append(errs, val)
		case "rerun-if-changed", "rerun-if-env-changed":
		case "metadata":
			k, v, _ := strings.Cut(val, "=")
			d.Metadata[k] = v
		default:
			if newSyntax {
				d.Unsupported = append(d.Unsupported, key)
			} else {
				d.Metadata[key] = val // old syntax: cargo:KEY=VALUE is links metadata
			}
		}
	}
	if len(errs) > 0 {
		return d, fmt.Errorf("build script reported errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return d, nil
}
