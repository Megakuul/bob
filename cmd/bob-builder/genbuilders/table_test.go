package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTableSourceHidesStorePath (A453): a bob built by bob embeds builders.go; the builder's
// store path must not appear in the binary as one string, or bob's output refers to the
// bob-builder that built it and the reference check stops the self-build (BOB-E-9003).
func TestTableSourceHidesStorePath(t *testing.T) {
	const arm = "/nix/store/hs90yr9c8s1zm190cxnxpw29885d4hsg-bob-builder-17"
	const amd = "/nix/store/3pcfi7sfqz8kcr39szr3rzzbhpfw1sw2-bob-builder-17"
	var cases string
	for _, c := range []struct{ system, path string }{{"aarch64-linux", arm}, {"x86_64-linux", amd}} {
		e, err := tableCase(c.system, c.path)
		if err != nil {
			t.Fatal(err)
		}
		cases += e
	}
	src, err := tableSource(cases)
	if err != nil {
		t.Fatal(err)
	}
	if got := tableOf(string(src)); got["aarch64-linux"] != arm || got["x86_64-linux"] != amd {
		t.Errorf("tableOf(generated) = %v", got)
	}
	// The single-string form of earlier tables (the -base copy) still parses.
	if got := tableOf(table(arm1, amd1)); got["aarch64-linux"] != arm1 || got["x86_64-linux"] != amd1 {
		t.Errorf("tableOf(old form) = %v", got)
	}
	if bytes.Contains(src, []byte("hs90yr9c8s1zm190cxnxpw29885d4hsg")) {
		t.Fatalf("generated source holds the whole hash:\n%s", src)
	}

	goBin, err := exec.LookPath("go")
	if err != nil || testing.Short() {
		t.Skip("compiling the table needs the go command")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":            "module example.com/tbl\n\ngo 1.26\n",
		"store/builders.go": string(src),
		"store/use.go":      "package store\n\n// Path is builderTable.\nfunc Path(s string) string { return builderTable(s) }\n",
		"main.go":           "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\"example.com/tbl/store\"\n)\n\nfunc main() { fmt.Print(store.Path(os.Args[1])) }\n",
	}
	for name, c := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(goBin, "build", "-trimpath", "-o", "tbl", ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	bin, err := os.ReadFile(filepath.Join(dir, "tbl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"hs90yr9c8s1zm190cxnxpw29885d4hsg", "3pcfi7sfqz8kcr39szr3rzzbhpfw1sw2"} {
		if bytes.Contains(bin, []byte(h)) {
			t.Errorf("the compiled binary contains the store hash %s", h)
		}
	}
	out, err := exec.Command(filepath.Join(dir, "tbl"), "aarch64-linux").Output()
	if err != nil || string(out) != arm {
		t.Errorf("builderTable(aarch64-linux) = %q, %v; want %s", out, err, arm)
	}
}
