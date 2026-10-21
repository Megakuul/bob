// Command bob-builder is the builder of every Rust derivation in spike S3 (static Go binary,
// no shell). It reads one bspec job from $bobSpec and runs it.
//
//	bob-builder rustc       compile one crate
//	bob-builder build-run   run a compiled build script and translate its cargo: directives
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"bobspike/s03/bspec"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: bob-builder rustc|build-run")
	}
	var err error
	switch os.Args[1] {
	case "rustc":
		var s bspec.Rustc
		load(&s)
		err = doRustc(&s)
	case "build-run":
		var s bspec.BuildRun
		load(&s)
		err = doBuildRun(&s)
	case "fetch-crate":
		var s bspec.FetchCrate
		load(&s)
		err = doFetchCrate(&s)
	case "cc":
		var s bspec.CC
		load(&s)
		err = doCC(&s)
	case "go":
		var s bspec.Go
		load(&s)
		err = doGo(&s)
	default:
		err = fmt.Errorf("unknown job %s", os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "bob-builder: "+msg)
	os.Exit(1)
}

func load(v any) {
	if err := json.Unmarshal([]byte(os.Getenv("bobSpec")), v); err != nil {
		fail("bad bobSpec: " + err.Error())
	}
}

func out() string { return os.Getenv("out") }

func srcDir(s bspec.Source) string { return s.Dir }

func readDirectives(buildRun string) (*bspec.Directives, error) {
	b, err := os.ReadFile(filepath.Join(buildRun, "directives.json"))
	if err != nil {
		return nil, err
	}
	var d bspec.Directives
	return &d, json.Unmarshal(b, &d)
}

func linking(t string) bool {
	return t == "bin" || t == "cdylib" || t == "staticlib" || t == "proc-macro"
}

func doRustc(s *bspec.Rustc) error {
	src := srcDir(s.Src)
	o := out()
	outDir := filepath.Join(o, "lib")
	if s.CrateType == "bin" {
		outDir = filepath.Join(o, "bin")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	args := []string{"--crate-name", s.CrateName, "--edition", s.Edition, "--crate-type", s.CrateType,
		filepath.Join(src, s.Root), "--emit=link", "-C", "opt-level=" + s.OptLevel, "-C", "metadata=" + s.Metadata,
		"--out-dir", outDir, "--remap-path-prefix", src + "=" + remapName(s), "-C", "strip=debuginfo"}
	switch s.CrateType {
	case "lib", "proc-macro":
		args = append(args, "-C", "extra-filename=-"+s.Metadata, "-C", "embed-bitcode=no")
	}
	if s.CrateType == "proc-macro" {
		args = append(args, "--extern", "proc_macro")
	}
	if s.CapLints {
		args = append(args, "--cap-lints", "allow")
	}
	for _, f := range s.Features {
		args = append(args, "--cfg", fmt.Sprintf("feature=%q", f))
	}
	for _, c := range s.CheckCfg {
		args = append(args, "--check-cfg", c)
	}
	for _, e := range s.Externs {
		args = append(args, "--extern", e.Name+"="+filepath.Join(e.Path, "lib", e.Lib))
	}
	for _, d := range s.DepDirs {
		args = append(args, "-L", "dependency="+filepath.Join(d, "lib"))
	}
	env := os.Environ()
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	if s.BuildRun != "" {
		d, err := readDirectives(s.BuildRun)
		if err != nil {
			return err
		}
		od := filepath.Join(s.BuildRun, "out-dir")
		env = append(env, "OUT_DIR="+od)
		args = append(args, "--remap-path-prefix", od+"="+remapName(s)+"/out")
		for _, c := range d.Cfgs {
			args = append(args, "--cfg", c)
		}
		for _, c := range d.CheckCfgs {
			if len(s.CheckCfg) > 0 {
				args = append(args, "--check-cfg", c)
			}
		}
		for k, v := range d.Env {
			env = append(env, k+"="+v)
		}
		for _, l := range d.LinkLibs {
			args = append(args, "-l", l)
		}
		for _, l := range d.LinkSearch {
			args = append(args, "-L", l)
		}
		if s.CrateType == "bin" || s.CrateType == "cdylib" {
			for _, a := range d.LinkArgs {
				args = append(args, "-C", "link-arg="+a)
			}
		}
	}
	if linking(s.CrateType) {
		seen := map[string]bool{}
		for _, br := range s.LinkFrom {
			d, err := readDirectives(br)
			if err != nil {
				return err
			}
			for _, l := range d.LinkSearch {
				if !seen[l] {
					seen[l] = true
					args = append(args, "-L", l)
				}
			}
		}
	}
	for _, n := range s.NativeLib {
		args = append(args, "-L", "native="+filepath.Join(n.Dir, "lib"), "-l", n.Kind+"="+n.Name)
	}
	args = append(args, s.ExtraArgs...)
	cmd := exec.Command("rustc", args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if os.Getenv("bobVerbose") != "" {
		fmt.Fprintln(os.Stderr, "bob-builder: rustc", strings.Join(args, " "))
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rustc failed for crate %s: %v", s.CrateName, err)
	}
	if s.Headers != "" {
		if err := copyTree(filepath.Join(src, s.Headers), filepath.Join(o, "include")); err != nil {
			return err
		}
	}
	return nil
}

func remapName(s *bspec.Rustc) string {
	if v := s.Env["CARGO_PKG_VERSION"]; v != "" {
		return "/rust/" + s.Env["CARGO_PKG_NAME"] + "-" + v
	}
	return "/rust/" + s.CrateName
}

func copyTree(from, to string) error {
	return filepath.Walk(from, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		t := filepath.Join(to, rel)
		if fi.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}

func doBuildRun(s *bspec.BuildRun) error {
	o := out()
	od := filepath.Join(o, "out-dir")
	if err := os.MkdirAll(od, 0o755); err != nil {
		return err
	}
	src := srcDir(s.Src)
	rustc, err := exec.LookPath("rustc")
	if err != nil {
		return err
	}
	env := os.Environ()
	set := map[string]string{"OUT_DIR": od, "CARGO_MANIFEST_DIR": src, "CARGO_MANIFEST_PATH": filepath.Join(src, "Cargo.toml"),
		"RUSTC": rustc, "RUSTDOC": filepath.Join(filepath.Dir(rustc), "rustdoc"), "NUM_JOBS": fmt.Sprint(runtime.NumCPU()),
		"CARGO_ENCODED_RUSTFLAGS": "", "CC": "cc", "CXX": "c++", "AR": "ar", "HOME": os.Getenv("TMPDIR")}
	if j := os.Getenv("NIX_BUILD_CORES"); j != "" && j != "0" {
		set["NUM_JOBS"] = j
	}
	for k, v := range s.Env {
		set[k] = v
	}
	for _, dl := range s.DepLinks {
		d, err := readDirectives(dl.Out)
		if err != nil {
			return err
		}
		pre := "DEP_" + envName(dl.Links) + "_"
		for k, v := range d.Metadata {
			set[pre+envName(k)] = v
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+set[k])
	}
	script, err := filepath.Glob(filepath.Join(s.Script, "bin", "*"))
	if err != nil || len(script) != 1 {
		return fmt.Errorf("build script binary not found in %s", s.Script)
	}
	cmd := exec.Command(script[0])
	cmd.Dir = src
	cmd.Env = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	if err := os.WriteFile(filepath.Join(o, "output"), stdout.Bytes(), 0o644); err != nil {
		return err
	}
	d, perr := parseDirectives(stdout.Bytes())
	for _, w := range d.Warnings {
		fmt.Fprintln(os.Stderr, "warning (build script):", w)
	}
	if runErr != nil {
		os.Stderr.Write(tail(stdout.Bytes(), 40))
		return fmt.Errorf("build script failed: %v", runErr)
	}
	if perr != nil {
		return perr
	}
	b, _ := json.MarshalIndent(d, "", " ")
	return os.WriteFile(filepath.Join(o, "directives.json"), b, 0o644)
}

func tail(b []byte, n int) []byte {
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return bytes.Join(lines, []byte("\n"))
}

func envName(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

// parseDirectives implements the build-script output contract (Cargo reference,
// "Build Scripts" → "Outputs of the Build Script"), old `cargo:` and new `cargo::` syntax.
func parseDirectives(out []byte) (*bspec.Directives, error) {
	d := &bspec.Directives{Env: map[string]string{}, Metadata: map[string]string{}}
	var errs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		var rest string
		newSyntax := false
		switch {
		case strings.HasPrefix(line, "cargo::"):
			rest, newSyntax = line[7:], true
		case strings.HasPrefix(line, "cargo:"):
			rest = line[6:]
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
				if (flag == "-l" || flag == "-L") && i+1 < len(f) {
					v = f[i+1]
					i++
				} else if strings.HasPrefix(flag, "-l") || strings.HasPrefix(flag, "-L") {
					flag, v = flag[:2], flag[2:]
				} else {
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
			// Nix rebuilds when inputs change; nothing to track
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

func run(name string, args []string, env []string, dir string) error {
	cmd := exec.Command(name, args...)
	cmd.Env, cmd.Dir = env, dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return nil
}

func doCC(s *bspec.CC) error {
	o := out()
	var flags, libs []string
	if s.Hdrs != "" {
		flags = append(flags, "-I"+filepath.Join(s.Src, s.Hdrs))
	}
	for _, d := range s.Deps {
		flags = append(flags, "-I"+filepath.Join(d.Dir, "include"))
		libs = append(libs, "-L"+filepath.Join(d.Dir, "lib"), "-l"+d.Name)
	}
	tmp := os.Getenv("TMPDIR")
	var objs []string
	for i, f := range s.Srcs {
		obj := filepath.Join(tmp, fmt.Sprintf("%d.o", i))
		if err := run("cc", append(append([]string{"-O2", "-fPIC"}, flags...), "-c", filepath.Join(s.Src, f), "-o", obj), os.Environ(), tmp); err != nil {
			return err
		}
		objs = append(objs, obj)
	}
	switch s.Kind {
	case "static":
		_ = os.MkdirAll(filepath.Join(o, "lib"), 0o755)
		if err := run("ar", append([]string{"rcs", filepath.Join(o, "lib", "lib"+s.Name+".a")}, objs...), os.Environ(), tmp); err != nil {
			return err
		}
	case "shared":
		_ = os.MkdirAll(filepath.Join(o, "lib"), 0o755)
		if err := run("cc", append(append([]string{"-shared", "-Wl,-soname,lib" + s.Name + ".so", "-o", filepath.Join(o, "lib", "lib"+s.Name+".so")}, objs...), libs...), os.Environ(), tmp); err != nil {
			return err
		}
	case "bin":
		_ = os.MkdirAll(filepath.Join(o, "bin"), 0o755)
		args := append(append([]string{"-o", filepath.Join(o, "bin", s.Name)}, objs...), libs...)
		if err := run("cc", append(args, s.LinkArgs...), os.Environ(), tmp); err != nil {
			return err
		}
	}
	if s.Hdrs != "" {
		return copyTree(filepath.Join(s.Src, s.Hdrs), filepath.Join(o, "include"))
	}
	return nil
}

func doGo(s *bspec.Go) error {
	o := out()
	tmp := os.Getenv("TMPDIR")
	mod := filepath.Join(tmp, "mod")
	if err := copyTree(s.Src, mod); err != nil {
		return err
	}
	var cf, lf []string
	for _, d := range s.Deps {
		cf = append(cf, "-I"+filepath.Join(d.Dir, "include"))
		lf = append(lf, "-L"+filepath.Join(d.Dir, "lib"), "-l"+d.Name)
	}
	lf = append(lf, s.LdExtra...)
	env := append(os.Environ(), "CGO_ENABLED=1", "CC=cc", "GOCACHE="+tmp+"/gocache", "GOPATH="+tmp+"/gopath",
		"HOME="+tmp, "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPROXY=off", "CGO_CFLAGS="+strings.Join(cf, " "),
		"CGO_LDFLAGS="+strings.Join(lf, " "))
	return run("go", []string{"build", "-trimpath", "-o", filepath.Join(o, "bin", s.Name), s.Main}, env, mod)
}

func doFetchCrate(s *bspec.FetchCrate) error {
	o := out()
	os.Setenv("SSL_CERT_FILE", s.CACert)
	resp, err := http.Get(s.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %s", s.URL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	if got := "sha256-" + base64.StdEncoding.EncodeToString(h[:]); got != s.Checksum {
		return fmt.Errorf("download %s does not match bob.lock: want %s got %s", s.URL, s.Checksum, got)
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hd.Name, "./"))
		_, rel, ok := strings.Cut(name, "/")
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "..") || strings.Contains(rel, "../") {
			return fmt.Errorf("unsafe path %q in crate", hd.Name)
		}
		if !ok {
			if hd.Typeflag == tar.TypeDir {
				continue
			}
			return fmt.Errorf("unexpected top-level entry %q", hd.Name)
		}
		dst := filepath.Join(o, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			mode := os.FileMode(0o644)
			if hd.Mode&0o100 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.Symlink(hd.Linkname, dst); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			return fmt.Errorf("unsupported tar entry %q", hd.Name)
		}
	}
	return os.MkdirAll(o, 0o755)
}
