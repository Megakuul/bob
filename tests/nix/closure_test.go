package nix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// buildPath builds label in fixture and returns the store path of its (first) output as the
// `package.finish` event reports it with --print-paths (a host path below <home>/nix/store).
func (e *bobEnv) buildPath(t testing.TB, fixture, label string) (path string, r harness.Result) {
	t.Helper()
	r = e.run(t, fixture, "build", "--json", "--print-paths", label)
	if r.IsNotImplemented() {
		return "", r
	}
	if r.Exit != 0 {
		t.Fatalf("bob build %s exit %d:\n%s", label, r.Exit, r.Combined())
	}
	for _, ev := range events(t, r.Stdout) {
		if l, _ := ev["label"].(string); ev["event"] == "package.finish" && strings.HasPrefix(l, label) {
			if ps, _ := ev["paths"].([]any); len(ps) > 0 {
				return "/nix/store/" + filepath.Base(ps[0].(string)), r
			}
		}
	}
	t.Fatalf("no paths[] in the package.finish event of %s:\n%s", label, r.Stdout)
	return "", r
}

// closureOf returns the closure of path (store paths, with NAR sizes) from Nix's own view of
// bob's store (`nix path-info --recursive --json`).
func (e *bobEnv) closureOf(t testing.TB, path string) (names []string, narSize int64) {
	t.Helper()
	out, err := e.hostNix(t, nil, "path-info", "--recursive", "--json", path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]struct {
		NarSize int64 `json:"narSize"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil || len(m) == 0 {
		var arr []struct {
			Path    string `json:"path"`
			NarSize int64  `json:"narSize"`
		}
		if err2 := json.Unmarshal([]byte(out), &arr); err2 != nil {
			t.Fatalf("harness: unexpected `nix path-info --json` output: %v / %v", err, err2)
		}
		for _, a := range arr {
			names = append(names, a.Path)
			narSize += a.NarSize
		}
		return names, narSize
	}
	for p, v := range m {
		names = append(names, p)
		narSize += v.NarSize
	}
	return names, narSize
}

var forbiddenClosure = regexp.MustCompile(`^(/nix/store/)?[0-9a-z]{32}-(gcc-[0-9][0-9.]*|gcc-wrapper-.*|cc-wrapper-.*|binutils-.*|rustc-.*|go-[0-9].*|ocaml-.*|bob-builder-.*|.*-src)$`)

// TestRNIX019PolyglotClosure: examples/polyglot native //cmd/api:api has no compiler, wrapper,
// builder or -src path in its closure (only gcc's -lib output), and its binary has no
// .debug_info.
func TestRNIX019PolyglotClosure(t *testing.T) {
	parity.Covers(t, "R-NIX-019")
	e := newBobEnv(t)
	p, r := e.buildPath(t, "polyglot", "//cmd/api:api")
	if harness.Red(t, r) {
		return
	}
	names, _ := e.closureOf(t, p)
	sawLib := false
	for _, n := range names {
		if forbiddenClosure.MatchString(n) {
			t.Errorf("closure of //cmd/api:api contains %s", n)
		}
		if strings.Contains(n, "-gcc-") && strings.HasSuffix(n, "-lib") {
			sawLib = true
		}
	}
	if !sawLib {
		t.Logf("note: no gcc -lib path in the closure (%d paths)", len(names))
	}
	bin := filepath.Join(e.Home, p, "bin", "api")
	if s := sections(t, bin); strings.Contains(s, ".debug_info") {
		t.Errorf("bin/api has .debug_info:\n%s", s)
	}
}

// TestRNIX019FileMacroDoesNotReferenceSources: a C program embedding __FILE__ of a source path
// builds without a reference to the source path (prevention by -ffile-prefix-map, backed by the
// reference check).
func TestRNIX019FileMacroDoesNotReferenceSources(t *testing.T) {
	parity.Covers(t, "R-NIX-019")
	e := newBobEnv(t)
	dir := e.dir(t, "cc")
	mainC := filepath.Join(dir, "app", "main.c")
	src := string(mustRead(t, mainC))
	if !strings.Contains(src, "int main(void) {") {
		t.Fatal("harness: cc/app/main.c changed shape")
	}
	src = strings.Replace(src, "int main(void) {", "volatile const char *bob_where = __FILE__;\n\nint main(void) {\n    (void)bob_where;", 1)
	mustWrite(t, mainC, []byte(src), 0o644)
	p, r := e.buildPath(t, "cc", "//app:app")
	if harness.Red(t, r) {
		return
	}
	names, _ := e.closureOf(t, p)
	for _, n := range names {
		if strings.HasSuffix(n, "-src") || strings.Contains(n, "-src-") {
			t.Errorf("closure contains the source path %s", n)
		}
	}
	b := mustRead(t, filepath.Join(e.Home, p, "bin", "app"))
	if m := regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^\x00 ]*-src[^\x00 ]*`).Find(b); m != nil {
		t.Errorf("the binary embeds the source path %q", m)
	}
	if _, err := os.Stat(filepath.Join(e.Home, p, "bin", "app")); err != nil {
		t.Error(err)
	}
}

const mib = 1 << 20

// TestRNIX030ClosureBudgets measures `nix path-info -S` equivalents (closure NAR size) for the
// rows of the table on this host's native architecture (budgets B-010 .. B-013; the other
// architecture's native rows run on its own hosts).
func TestRNIX030ClosureBudgets(t *testing.T) {
	parity.Covers(t, "R-NIX-030")
	t.Run("native-closure", func(t *testing.T) {
		e := newBobEnv(t)
		p, r := e.buildPath(t, "polyglot", "//cmd/api:api")
		if harness.Red(t, r) {
			return
		}
		names, size := e.closureOf(t, p)
		if size > 74*mib {
			t.Errorf("native //cmd/api:api closure is %.1f MiB, budget 74 MiB", float64(size)/mib)
		}
		if len(names) > 12 {
			t.Errorf("native //cmd/api:api closure has %d paths, budget 12", len(names))
		}
		fi, err := os.Stat(filepath.Join(e.Home, p, "bin", "api"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 5.5*mib {
			t.Errorf("api binary is %.2f MiB, budget 5.5 MiB", float64(fi.Size())/mib)
		}
	})
	t.Run("cross-native-libs", func(t *testing.T) {
		e := newBobEnv(t)
		p, r := e.buildPath(t, "polyglot", "//cmd/api:api@linux-amd64")
		if harness.Red(t, r) {
			return
		}
		if _, size := e.closureOf(t, p); size > 112*mib {
			t.Errorf("cross //cmd/api:api@linux-amd64 closure is %.1f MiB, budget 112 MiB", float64(size)/mib)
		}
	})
	t.Run("cross-Cross()", func(t *testing.T) {
		e := newBobEnv(t)
		foo := filepath.Join(e.dir(t, "polyglot"), "libs", "foo", "bob.go")
		s := string(mustRead(t, foo))
		if !strings.Contains(s, `nixpkgs.Lib(p, "openssl")`) {
			t.Fatal("harness: polyglot libs/foo no longer links nixpkgs openssl")
		}
		mustWrite(t, foo, []byte(strings.Replace(s, `nixpkgs.Lib(p, "openssl")`, `nixpkgs.Lib(p, "openssl", nixpkgs.Cross())`, 1)), 0o644)
		p, r := e.buildPath(t, "polyglot", "//cmd/api:api@linux-amd64")
		if harness.Red(t, r) {
			return
		}
		if _, size := e.closureOf(t, p); size > 74*mib {
			t.Errorf("cross //cmd/api:api@linux-amd64 with Cross() libs: closure is %.1f MiB, budget 74 MiB", float64(size)/mib)
		}
	})
}
