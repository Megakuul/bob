package core

// SPEC §1.1 Repository layout and layering.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

const module = "github.com/megakuul/bob"

func goCmd(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = harness.Root(t)
	cmd.Env = append(os.Environ(), env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out) + stderr.String(), err
	}
	return string(out), nil
}

// R-ARCH-001: every package of `go list ./...` is under pkg/, cmd/, internal/ or proto/, and
// go.mod's module line is `module github.com/megakuul/bob`. Decision (HARNESS.md §16 [open] 4,
// auto-decided A24): the list is taken with GOWORK=off, so the tools/ module is outside, and
// tests/ (test-only packages, never imported by cmd/) is allowed.
func TestRARCH001MonorepoLayout(t *testing.T) {
	parity.Covers(t, "R-ARCH-001")
	gomod, err := os.ReadFile(filepath.Join(harness.Root(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^module github\.com/megakuul/bob$`).Match(gomod) {
		t.Errorf("go.mod has no line %q", "module "+module)
	}
	out, err := goCmd(t, []string{"GOWORK=off"}, "list", "./...")
	if err != nil {
		t.Fatalf("GOWORK=off go list ./...: %v\n%s", err, out)
	}
	allowed := []string{"pkg/", "cmd/", "internal/", "proto/", "tests/"}
	for _, p := range lines(out) {
		rel, ok := strings.CutPrefix(p, module+"/")
		if !ok {
			t.Errorf("package %s is outside the module path", p)
			continue
		}
		okDir := false
		for _, a := range allowed {
			okDir = okDir || strings.HasPrefix(rel+"/", a)
		}
		if !okDir {
			t.Errorf("package %s is not under pkg/, cmd/, internal/, proto/ (or tests/)", p)
		}
	}
	for _, top := range []string{"pkg", "cmd", "internal", "proto", "web"} {
		if info, err := os.Stat(filepath.Join(harness.Root(t), top)); err != nil || !info.IsDir() {
			t.Errorf("top-level directory %s/ is missing", top)
		}
	}
}

// R-ARCH-002 and R-ARCH-003 are encoded once, in pkg/layering_test.go (TestLayering,
// TestLayeringRejects). This wrapper maps them in the parity catalogue without editing that
// protected file, and fails exactly when they fail.
func TestRARCH002003Layering(t *testing.T) {
	parity.Covers(t, "R-ARCH-002", "R-ARCH-003")
	out, err := goCmd(t, nil, "test", "-count=1", "-run", "^(TestLayering|TestLayeringRejects)$", "-v", "./pkg")
	if err != nil {
		t.Fatalf("go test ./pkg -run TestLayering failed: %v\n%s", err, out)
	}
	for _, name := range []string{"TestLayering", "TestLayeringRejects"} {
		if !strings.Contains(out, "--- PASS: "+name+" ") {
			t.Errorf("%s did not run and pass:\n%s", name, out)
		}
	}
	// R-ARCH-002's own command, independent of the layer table encoding.
	deps, err := goCmd(t, nil, "list", "-deps", "-f", "{{.ImportPath}}", "./pkg/...", "./cmd/bob", "./cmd/bob-builder")
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, deps)
	}
	server := regexp.MustCompile(`^github.com/megakuul/bob/(internal|proto|cmd/bob-server)(/|$)`)
	for _, p := range lines(deps) {
		if server.MatchString(p) {
			t.Errorf("the framework depends on server package %s (R-ARCH-002)", p)
		}
	}
}

// R-ARCH-004: `go list -deps ./cmd/bob` contains no internal/ package; `bob --help` lists no
// server command.
func TestRARCH004TwoBinaries(t *testing.T) {
	parity.Covers(t, "R-ARCH-004")
	deps, err := goCmd(t, nil, "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/bob")
	if err != nil {
		t.Fatalf("go list -deps ./cmd/bob: %v\n%s", err, deps)
	}
	for _, p := range lines(deps) {
		if strings.HasPrefix(p, module+"/internal/") || p == module+"/internal" {
			t.Errorf("cmd/bob depends on %s", p)
		}
	}
	for _, c := range []string{"./cmd/bob", "./cmd/bob-server", "./cmd/bob-builder"} {
		if out, err := goCmd(t, nil, "list", "-f", "{{.Name}}", c); err != nil || strings.TrimSpace(out) != "main" {
			t.Errorf("%s is not a main package: %v %s", c, err, out)
		}
	}
	r := bobRun(t, t.TempDir(), "--help")
	if !exitIs(t, r, 0) {
		return
	}
	// bob-server's modes (SPEC §05/§06) and anything named like the server.
	cmdLine := regexp.MustCompile(`^\s{2}(\S+)\s`)
	listed := 0
	for _, l := range lines(r.Stdout) {
		m := cmdLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		listed++
		switch name := m[1]; {
		case name == "orchestrator", name == "worker", name == "admin", strings.Contains(name, "server"):
			t.Errorf("bob --help lists the server command %q: %q", name, l)
		}
	}
	if listed == 0 {
		t.Errorf("bob --help lists no commands at all (the scan would be vacuous):\n%s", r.Stdout)
	}
	if strings.Contains(r.Stdout, "bob-server") {
		t.Errorf("bob --help mentions bob-server:\n%s", r.Stdout)
	}
}

// R-ARCH-005: in a clean checkout, scripts/bootstrap.sh ends with a bob built by bob whose
// embedded builder store path equals the one in pkg/engine/store/builders.go.
func TestRARCH005Bootstrap(t *testing.T) {
	parity.Covers(t, "R-ARCH-005")
	root := harness.Root(t)
	if _, err := os.Stat(filepath.Join(root, "scripts", "bootstrap.sh")); os.IsNotExist(err) {
		harness.RedFeature(t, "scripts/bootstrap.sh (R-ARCH-005)")
		return
	}
	// A clean checkout of HEAD: a git worktree, so local edits do not leak in.
	wt := filepath.Join(t.TempDir(), "checkout")
	git(t, root, "worktree", "add", "--detach", wt, "HEAD")
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", wt)
		cmd.Dir = root
		_ = cmd.Run()
	})
	home := t.TempDir()
	cmd := exec.Command("bash", "scripts/bootstrap.sh")
	cmd.Dir = wt
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "BOB_HOME=" + filepath.Join(home, "bob"), "TZ=UTC", "LC_ALL=C"}
	r := runCmd(t, cmd, []string{"scripts/bootstrap.sh"})
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	// Decision: the script's last stdout line is the path of the bob that bob built.
	out := lines(r.Stdout)
	if len(out) == 0 {
		t.Fatalf("scripts/bootstrap.sh printed nothing")
	}
	final := strings.TrimSpace(out[len(out)-1])
	if !filepath.IsAbs(final) {
		t.Fatalf("scripts/bootstrap.sh: last line %q is not the path of the final bob", final)
	}
	builders := readFile(t, filepath.Join(wt, "pkg", "engine", "store", "builders.go"))
	// {system → store path} for both systems.
	for _, sys := range []string{`"aarch64-linux"`, `"x86_64-linux"`} {
		if !strings.Contains(builders, sys) {
			t.Errorf("pkg/engine/store/builders.go has no entry for %s", sys)
		}
	}
	if n := len(regexp.MustCompile(`/nix/store/[0-9a-z]{32}-bob-builder-\d+`).FindAllString(builders, -1)); n < 2 {
		t.Errorf("pkg/engine/store/builders.go lists %d bob-builder store paths, want one per system", n)
	}
	// At first use bob imports its embedded builder and refuses to run when the resulting
	// store path differs from builders.go (R-NIX-015, BOB-E-9007). A clean doctor run of the
	// bootstrapped bob therefore proves the paths are equal.
	dr := harness.RunPath(t, final, harness.Spec{Dir: wt, Env: []string{"BOB_HOME=" + filepath.Join(home, "bob")}, Timeout: 5 * time.Minute}, "doctor")
	if strings.Contains(dr.Combined(), "BOB-E-9007") {
		t.Errorf("the bootstrapped bob's embedded builder does not match builders.go:\n%s", dr.Combined())
	}
	exitIs(t, dr, 0)
}
