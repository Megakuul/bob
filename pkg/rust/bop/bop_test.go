package bop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseDirectives(t *testing.T) {
	out := strings.Join([]string{
		"cargo::rustc-cfg=corelib_fast",
		"cargo::rustc-check-cfg=cfg(corelib_fast)",
		"cargo::rustc-env=CORELIB_BUILT_BY=build.rs",
		"cargo:rustc-link-lib=static=zstd",
		"cargo:rustc-link-search=native=/out",
		"cargo:rustc-flags=-l dylib=ssl -L/usr/x",
		"cargo:include=/src/zstd/lib",
		"cargo::metadata=root=/out",
		"cargo::rerun-if-changed=build.rs",
		"cargo::warning=careful",
		"cargo::frobnicate=1",
		"not a directive",
	}, "\n")
	d, err := ParseDirectives([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Cfgs, []string{"corelib_fast"}) || !slices.Equal(d.CheckCfgs, []string{"cfg(corelib_fast)"}) {
		t.Errorf("cfgs %v / %v", d.Cfgs, d.CheckCfgs)
	}
	if d.Env["CORELIB_BUILT_BY"] != "build.rs" {
		t.Errorf("env %v", d.Env)
	}
	if !slices.Equal(d.LinkLibs, []string{"static=zstd", "dylib=ssl"}) || !slices.Equal(d.LinkSearch, []string{"native=/out", "/usr/x"}) {
		t.Errorf("link %v / %v", d.LinkLibs, d.LinkSearch)
	}
	if d.Metadata["include"] != "/src/zstd/lib" || d.Metadata["root"] != "/out" {
		t.Errorf("metadata %v", d.Metadata)
	}
	if !slices.Equal(d.Warnings, []string{"careful"}) || !slices.Equal(d.Unsupported, []string{"frobnicate"}) {
		t.Errorf("warnings %v unsupported %v", d.Warnings, d.Unsupported)
	}
	if _, err := ParseDirectives([]byte("cargo::error=no zstd\n")); err == nil || !strings.Contains(err.Error(), "no zstd") {
		t.Errorf("cargo::error must fail the run: %v", err)
	}
}

func TestRustcArgv(t *testing.T) {
	run := t.TempDir()
	d := Directives{Cfgs: []string{"x"}, CheckCfgs: []string{"cfg(x)"}, Env: map[string]string{"K": "V"},
		LinkLibs: []string{"static=z"}, LinkSearch: []string{"native=/o"}, LinkArgs: []string{"-Wl,-z,now"}}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(filepath.Join(run, "directives.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	a := &RustcArgs{Toolchain: Toolchain{Rustc: "/r/bin/rustc"}, Src: "/s", Remap: "corelib", CrateName: "corelib",
		CrateType: "bin", Edition: "2021", Root: "src/main.rs", Features: []string{"fast"}, Metadata: "0123456789abcdef",
		OptLevel: "3", CheckCfg: true, FeatureSet: []string{"fast", "hexout"},
		Externs: []Extern{{Name: "itoa", File: "/d/lib/libitoa-1.rlib"}}, DepDirs: []string{"/d/lib"}, BuildRun: run,
		LinkFrom: []string{run}, OutName: "demo"}
	argv, env, err := rustcArgv(a, "/out")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(argv, " ")
	for _, want := range []string{"--crate-type bin", "/s/src/main.rs", "-o /out/bin/demo", "--remap-path-prefix /s=corelib",
		`--cfg feature="fast"`, `--check-cfg cfg(feature, values("fast", "hexout"))`, "--extern itoa=/d/lib/libitoa-1.rlib",
		"-L dependency=/d/lib", "--cfg x", "--check-cfg cfg(x)", "-l static=z", "-L native=/o", "-C link-arg=-Wl,-z,now"} {
		if !strings.Contains(s, want) {
			t.Errorf("argv lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "extra-filename") || strings.Contains(s, "--cap-lints") {
		t.Errorf("a local binary gets no extra-filename and no cap-lints:\n%s", s)
	}
	if env["OUT_DIR"] != filepath.Join(run, "out-dir") || env["K"] != "V" || env["CARGO_MANIFEST_DIR"] != "/s" {
		t.Errorf("env %v", env)
	}
}

func TestInvalidJobs(t *testing.T) {
	for _, raw := range []string{`{"unknown":1}`, `[]`} {
		var a RustcArgs
		if err := decode(json.RawMessage(raw), &a); err == nil {
			t.Errorf("%s decoded", raw)
		}
	}
}
