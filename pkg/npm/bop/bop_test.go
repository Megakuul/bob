package bop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder"
)

// runJob runs one job through builder.Main like Nix does: structured attributes in a file,
// NIX_BUILD_TOP, outputs as absolute paths.
func runJob(t *testing.T, top, kind string, args any, out string) (int, string) {
	t.Helper()
	job := map[string]any{"v": 1, "kind": kind, "label": "//t:t", "outputs": []string{"out"}, "args": args,
		"fixup": map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []string{},
			"tools": map[string]string{"strip": "", "objcopy": "", "patchelf": ""}}}
	attrs, err := json.Marshal(map[string]any{"bobSpec": job, "outputs": map[string]string{"out": out}})
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(top, ".attrs.json")
	if err := os.WriteFile(f, attrs, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIX_ATTRS_JSON_FILE", f)
	t.Setenv("NIX_BUILD_TOP", top)
	kinds := builder.Core()
	for k, v := range Kinds() {
		kinds[k] = v
	}
	var stdout, stderr bytes.Buffer
	code := builder.Main([]string{kind}, kinds, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

// tarball writes an npm tarball (top directory "package/") with files.
func tarball(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".tgz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readlink(t *testing.T, p string) string {
	t.Helper()
	s, err := os.Readlink(p)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	return s
}

func TestUnpackLinksAndCycleMembers(t *testing.T) {
	top := t.TempDir()
	tb := tarball(t, top, "a", map[string]string{"package.json": `{"name":"a","version":"1.0.0"}`, "index.js": "module.exports = 1;\n"})
	tb2 := tarball(t, top, "b", map[string]string{"package.json": `{"name":"@s/b","version":"1.0.0"}`})
	out := filepath.Join(top, "out")
	args := UnpackArgs{Members: []Member{
		{Dir: out + "/0", Name: "a", Tarball: tb, Links: []Link{{Alias: "@s/b", Target: out + "/1/node_modules/@s/b"}, {Alias: "a", Target: "/self"}}},
		{Dir: out + "/1", Name: "@s/b", Tarball: tb2, Links: []Link{{Alias: "a", Target: out + "/0/node_modules/a"}}},
	}}
	if code, log := runJob(t, top, "npm-unpack", args, out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	if b, err := os.ReadFile(filepath.Join(out, "0/node_modules/a/index.js")); err != nil || !strings.Contains(string(b), "module.exports") {
		t.Errorf("a not unpacked: %v", err)
	}
	if got := readlink(t, filepath.Join(out, "0/node_modules/@s/b")); got != out+"/1/node_modules/@s/b" {
		t.Errorf("link @s/b = %s", got)
	}
	if got := readlink(t, filepath.Join(out, "1/node_modules/a")); got != out+"/0/node_modules/a" {
		t.Errorf("link a = %s", got)
	}
	if fi, err := os.Lstat(filepath.Join(out, "0/node_modules/a")); err != nil || !fi.IsDir() {
		t.Errorf("a link to itself replaced the package: %v", err)
	}
}

func TestUnpackRejectsEscapingAlias(t *testing.T) {
	top := t.TempDir()
	tb := tarball(t, top, "a", map[string]string{"package.json": `{"name":"a"}`})
	out := filepath.Join(top, "out")
	for _, alias := range []string{"../x", "a/b", "@s/../../x", ""} {
		args := UnpackArgs{Members: []Member{{Dir: out, Name: "a", Tarball: tb, Links: []Link{{Alias: alias, Target: "/t"}}}}}
		if code, log := runJob(t, top, "npm-unpack", args, out); code != builder.ExitInvalid {
			t.Errorf("alias %q: exit %d, want %d (invalid job)\n%s", alias, code, builder.ExitInvalid, log)
		}
		_ = os.RemoveAll(out)
	}
}

func TestScriptsRunAndKeepOnlyUsedAddons(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	top := t.TempDir()
	node := filepath.Join(top, "node") // only named in wrappers; the scripts do not run node
	tb := tarball(t, top, "addon", map[string]string{
		"package.json": `{"name":"addon","version":"2.0.0","scripts":{"preinstall":"echo pre > pre.txt","install":"mkdir -p build/Release/obj build/obj && touch build/Release/addon.node build/Release/test_ext.node build/Release/obj/x.o build/Makefile build/obj/y.o && echo $npm_config_build_from_source > env.txt"}}`,
		"index.js":     "module.exports = require('bindings')('addon.node');\n",
	})
	out := filepath.Join(top, "out")
	sc := &Script{Node: node, Bash: bash, Path: []string{filepath.Dir(bash), "/usr/bin", "/bin"}, Bins: []Bin{},
		Env: map[string]string{"npm_config_build_from_source": "true"}}
	args := UnpackArgs{Members: []Member{{Dir: out, Name: "addon", Tarball: tb, Links: []Link{}, Script: sc}}}
	if code, log := runJob(t, top, "npm-unpack", args, out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	pkg := filepath.Join(out, "node_modules", "addon")
	var kept []string
	_ = filepath.Walk(filepath.Join(pkg, "build"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(pkg, p)
			kept = append(kept, rel)
		}
		return nil
	})
	if len(kept) != 1 || kept[0] != "build/Release/addon.node" {
		t.Errorf("kept %v, want only build/Release/addon.node", kept)
	}
	for f, want := range map[string]string{"pre.txt": "pre", "env.txt": "true"} {
		if b, err := os.ReadFile(filepath.Join(pkg, f)); err != nil || strings.TrimSpace(string(b)) != want {
			t.Errorf("%s = %q, %v; want %q", f, b, err, want)
		}
	}
}

func TestFailingScriptFailsTheJob(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	top := t.TempDir()
	tb := tarball(t, top, "bad", map[string]string{"package.json": `{"name":"bad","scripts":{"postinstall":"exit 3"}}`})
	out := filepath.Join(top, "out")
	sc := &Script{Node: top, Bash: bash, Path: []string{}, Bins: []Bin{}, Env: map[string]string{}}
	args := UnpackArgs{Members: []Member{{Dir: out, Name: "bad", Tarball: tb, Links: []Link{}, Script: sc}}}
	code, log := runJob(t, top, "npm-unpack", args, out)
	if code != builder.ExitStep || !strings.Contains(log, "postinstall") {
		t.Errorf("exit %d, want %d naming the postinstall script:\n%s", code, builder.ExitStep, log)
	}
}

func TestNodeModulesIsolatedAndHoisted(t *testing.T) {
	top := t.TempDir()
	out := filepath.Join(top, "out")
	args := NodeModulesArgs{Out: out, Links: []Link{{Alias: "react", Target: "/nix/store/x-npm-react/node_modules/react"},
		{Alias: "@s/ui", Target: "/nix/store/y-npm-lib/node_modules/@s/ui"}}, Hoisted: []HoistEntry{},
		Bins: []Bin{{Name: "vite", Script: "/nix/store/z/node_modules/vite/bin/vite.js"}}, Node: "/n/bin/node", Bash: "/b/bin/bash"}
	if code, log := runJob(t, top, "npm-node-modules", args, out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	if got := readlink(t, filepath.Join(out, "@s/ui")); got != "/nix/store/y-npm-lib/node_modules/@s/ui" {
		t.Errorf("@s/ui → %s", got)
	}
	b, err := os.ReadFile(filepath.Join(out, ".bin", "vite"))
	if err != nil || string(b) != "#!/b/bin/bash\nexec '/n/bin/node' '/nix/store/z/node_modules/vite/bin/vite.js' \"$@\"\n" {
		t.Errorf(".bin/vite = %q, %v", b, err)
	}

	top2 := t.TempDir()
	tb := tarball(t, top2, "a", map[string]string{"package.json": `{"name":"a"}`})
	out2 := filepath.Join(top2, "out")
	h := NodeModulesArgs{Out: out2, Links: []Link{}, Bins: []Bin{}, Node: "/n", Bash: "/b", Hoisted: []HoistEntry{
		{Path: "a", Tarball: tb}, {Path: "a/node_modules/a", Tarball: tb}, {Path: "lib", Link: "/nix/store/l"}}}
	if code, log := runJob(t, top2, "npm-node-modules", h, out2); code != 0 {
		t.Fatalf("hoisted: exit %d:\n%s", code, log)
	}
	if _, err := os.Stat(filepath.Join(out2, "a/node_modules/a/package.json")); err != nil {
		t.Errorf("nested package missing: %v", err)
	}
	if got := readlink(t, filepath.Join(out2, "lib")); got != "/nix/store/l" {
		t.Errorf("lib → %s", got)
	}
	bad := h
	bad.Hoisted = []HoistEntry{{Path: "../x", Tarball: tb}}
	if code, _ := runJob(t, top2, "npm-node-modules", bad, filepath.Join(top2, "out3")); code != builder.ExitInvalid {
		t.Errorf("escaping hoisted path: exit %d, want %d", code, builder.ExitInvalid)
	}
}

func TestBuildStepsAndScrub(t *testing.T) {
	top := t.TempDir()
	out := filepath.Join(top, "out")
	src := filepath.Join(top, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := "0123456789abcdfghijklmnpqrsvwxyz"
	bundle := "// /nix/store/" + hash + "-npm-cookie-1.0/index.js\nimport x from \"/nix/store/" + hash + "-nodejs-24/x\";\n"
	args := BuildArgs{Steps: []Step{
		{Op: "copy", From: src, Path: top + "/w"},
		{Op: "write", Path: out + "/lib/main.mjs", Content: bundle},
		{Op: "write", Path: out + "/bin/app", Content: "#!x\n", Mode: 0o755},
		{Op: "symlink", Path: out + "/node_modules/a", Target: "/nix/store/t"},
		{Op: "exec", Path: top + "/w", Argv: []string{"/bin/sh", "-c", "test -f a.txt"}},
		{Op: "scrub", Path: out + "/lib"},
	}}
	if code, log := runJob(t, top, "npm-build", args, out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	b, _ := os.ReadFile(filepath.Join(out, "lib", "main.mjs"))
	if strings.Contains(string(b), hash+"-npm-") || !strings.Contains(string(b), strings.Repeat("e", 32)+"-npm-cookie") ||
		!strings.Contains(string(b), hash+"-nodejs-24") {
		t.Errorf("scrub: npm references must lose their hash, others stay:\n%s", b)
	}
	if fi, err := os.Stat(filepath.Join(out, "bin", "app")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("bin/app is not executable: %v", err)
	}
	if code, _ := runJob(t, top, "npm-build", BuildArgs{Steps: []Step{{Op: "rm", Path: out}}}, out); code != builder.ExitInvalid {
		t.Errorf("unknown op: exit %d, want %d", code, builder.ExitInvalid)
	}
}
