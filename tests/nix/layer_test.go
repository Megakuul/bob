package nix

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// nixFiles lists the .nix files below dir (slash-separated, relative to base).
func nixFiles(t testing.TB, dir, base string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".nix") {
			rel, _ := filepath.Rel(base, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestRNIX001OnlyResolveNix: `grep -r --include='*.nix' -l . pkg/` lists only
// pkg/engine/nixidx/resolve.nix, and `bob build -v` shows no nix eval / nix-instantiate except
// the index's.
func TestRNIX001OnlyResolveNix(t *testing.T) {
	parity.Covers(t, "R-NIX-001")
	root := harness.Root(t)
	got := nixFiles(t, filepath.Join(root, "pkg"), root)
	switch {
	case len(got) == 0:
		harness.RedFeature(t, "pkg/engine/nixidx/resolve.nix does not exist yet")
	case len(got) != 1 || got[0] != "pkg/engine/nixidx/resolve.nix":
		t.Errorf(".nix files below pkg/ = %v, want only pkg/engine/nixidx/resolve.nix", got)
	}
	e := newBobEnv(t)
	r := e.run(t, "polyglot", "build", "-v", "//...")
	if harness.Red(t, r) {
		return
	}
	re := regexp.MustCompile(`nix-instantiate|nix eval|nix-build |nix build .*\.nix|--expr`)
	for _, line := range strings.Split(r.Combined(), "\n") {
		if re.MatchString(line) && !strings.Contains(line, "resolve.nix") {
			t.Errorf("bob build -v shows a Nix evaluation that is not the index's: %q", line)
		}
	}
}

var nixWords = []string{"Derivation", "Drv", "StorePath", "Flake", "Expr"}

// exportedNames returns the exported identifiers (functions, methods, types, struct fields,
// interface methods, constants and variables) declared in the non-test Go files of dir.
func exportedNames(t testing.TB, dir string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	add := func(file, n string) {
		if ast.IsExported(n) {
			names[n] = file
		}
	}
	for _, de := range ents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".go") || strings.HasSuffix(de.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, de.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", de.Name(), err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				add(de.Name(), x.Name.Name)
			case *ast.TypeSpec:
				add(de.Name(), x.Name.Name)
			case *ast.ValueSpec:
				for _, id := range x.Names {
					add(de.Name(), id.Name)
				}
			case *ast.Field:
				for _, id := range x.Names {
					add(de.Name(), id.Name)
				}
			}
			return true
		})
	}
	return names
}

// TestRNIX013OnlyNixpkgsLeaks greps the exported identifiers of the public API packages (L0–L3)
// for Derivation, Drv, StorePath, Flake and Expr, and checks that no .nix file appears in any
// fixture repository, also after `bob dev`.
func TestRNIX013OnlyNixpkgsLeaks(t *testing.T) {
	parity.Covers(t, "R-NIX-013")
	root := harness.Root(t)
	var dirs []string
	for _, p := range []string{"pkg/bob", "pkg/nixpkgs", "pkg/cc", "pkg/golang", "pkg/rust", "pkg/npm", "pkg/ocaml",
		"pkg/proto", "pkg/connect", "pkg/graphql", "pkg/openapi", "pkg/services", "pkg/output", "pkg/release"} {
		_ = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(p)), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				switch d.Name() {
				case "gen", "bop", "diag", "internal", "testdata":
					return filepath.SkipDir // engine-side code, not the API users call
				}
				dirs = append(dirs, path)
			}
			return nil
		})
	}
	if len(dirs) < 10 {
		t.Fatalf("found only %d API package directories", len(dirs))
	}
	for _, dir := range dirs {
		for n, file := range exportedNames(t, dir) {
			for _, w := range nixWords {
				if strings.Contains(n, w) {
					rel, _ := filepath.Rel(root, filepath.Join(dir, file))
					t.Errorf("%s: exported identifier %q contains %q", rel, n, w)
				}
			}
		}
	}
	fixtures := filepath.Join(root, "tests", "fixtures", "repos")
	if got := nixFiles(t, fixtures, fixtures); len(got) > 0 {
		t.Errorf("fixture repositories contain .nix files: %v", got)
	}
	for _, fx := range []string{"acme", "cpp-lib"} {
		e := newBobEnv(t)
		r := e.run(t, fx, "dev", "--", "true")
		if harness.Red(t, r) {
			return
		}
		if got := nixFiles(t, e.dir(t, fx), e.dir(t, fx)); len(got) > 0 {
			t.Errorf("%s: .nix files after bob dev: %v", fx, got)
		}
	}
}

// fodDrvs returns the drvs in bob's store that are fixed-output (carry outputHash), with their
// content.
func (e *bobEnv) fodDrvs(t testing.TB) map[string][]byte {
	t.Helper()
	ents, err := os.ReadDir(e.storeDir())
	if err != nil {
		t.Fatalf("bob's store is missing after a successful build: %v", err)
	}
	out := map[string][]byte{}
	for _, de := range ents {
		if !strings.HasSuffix(de.Name(), ".drv") {
			continue
		}
		b := mustRead(t, filepath.Join(e.storeDir(), de.Name()))
		if bytes.Contains(b, []byte(`"outputHash"`)) || regexp.MustCompile(`\("out","[^"]*","(r:)?sha(256|512)","[0-9a-f]+"\)`).Match(b) {
			out[de.Name()] = b
		}
	}
	return out
}

// TestRNIX008NoUserFixedOutputDerivations: no derivation bob generates from the fixtures is a
// fixed-output derivation (only nixpkgs' own FODs may exist in the store), and a warm rebuild
// succeeds although every network route is dead.
func TestRNIX008NoUserFixedOutputDerivations(t *testing.T) {
	parity.Covers(t, "R-NIX-008")
	for _, fx := range []string{"cpp-lib", "go", "rust", "polyglot"} {
		t.Run(fx, func(t *testing.T) {
			e := newBobEnv(t)
			r := e.run(t, fx, "build", "//...")
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 0 {
				t.Fatalf("bob build exit %d:\n%s", r.Exit, r.Combined())
			}
			for name, b := range e.fodDrvs(t) {
				if bytes.Contains(b, []byte("bob-builder")) || bytes.Contains(b, []byte("bobSpec")) {
					t.Errorf("%s is a fixed-output derivation generated by bob", name)
				}
			}
			dead := []string{"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1",
				"http_proxy=http://127.0.0.1:1", "https_proxy=http://127.0.0.1:1", "NO_PROXY="}
			again := e.runEnv(t, fx, dead, "build", "//...")
			if again.Exit != 0 {
				t.Errorf("warm rebuild with every network route dead: exit %d\n%s", again.Exit, again.Combined())
			}
		})
	}
}

// lockFile reads bob.lock of dir.
func lockFile(t testing.TB, dir string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "bob.lock")), &m); err != nil {
		t.Fatalf("bob.lock is not JSON: %v", err)
	}
	return m
}

// TestRNIX005NixpkgsPin: a fresh repo with Nixpkgs "nixos-26.05@cd4ea8d5…": `bob lock` writes the
// pinned entry; a tampered narHash gives 3001 for nixpkgs.
func TestRNIX005NixpkgsPin(t *testing.T) {
	parity.Covers(t, "R-NIX-005")
	rev := nixpkgsRev(t)
	e := newBobEnv(t)
	dir := e.dir(t, "cpp-lib")
	bobgo := filepath.Join(dir, "bob.go")
	src := string(mustRead(t, bobgo))
	if !strings.Contains(src, `Nixpkgs: "nixos-26.05"`) {
		t.Fatal("harness: the cpp-lib fixture no longer declares Nixpkgs: \"nixos-26.05\"")
	}
	mustWrite(t, bobgo, []byte(strings.Replace(src, `Nixpkgs: "nixos-26.05"`, `Nixpkgs: "nixos-26.05@`+rev+`"`, 1)), 0o644)
	mustWrite(t, filepath.Join(dir, "bob.lock"), []byte("{\n  \"lockVersion\": 1\n}\n"), 0o644)
	r := e.run(t, "cpp-lib", "lock")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob lock exit %d:\n%s", r.Exit, r.Combined())
	}
	var pin struct {
		Channel string `json:"channel"`
		NarHash string `json:"narHash"`
		Rev     string `json:"rev"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(lockFile(t, dir)["nixpkgs"], &pin); err != nil {
		t.Fatalf("bob.lock has no nixpkgs entry: %v", err)
	}
	if pin.Rev != rev || pin.Channel != "nixos-26.05" || !strings.HasPrefix(pin.NarHash, "sha256-") ||
		!strings.Contains(pin.URL, rev) {
		t.Errorf("nixpkgs entry = %+v, want rev %s on channel nixos-26.05 with a sha256 narHash and a URL naming the rev", pin, rev)
	}
	// Tamper: replace the narHash by another well-formed SRI hash.
	lock := string(mustRead(t, filepath.Join(dir, "bob.lock")))
	other := "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if !strings.Contains(lock, pin.NarHash) {
		t.Fatal("harness: narHash not found in bob.lock text")
	}
	mustWrite(t, filepath.Join(dir, "bob.lock"), []byte(strings.Replace(lock, pin.NarHash, other, 1)), 0o644)
	e2 := newBobEnv(t) // a fresh store: the tampered hash must be checked on a real fetch
	e2.fixtures["cpp-lib"] = dir
	b := e2.run(t, "cpp-lib", "build", "//...")
	out := b.Combined()
	if b.Exit != 4 || !strings.Contains(out, "BOB-E-3001") || !strings.Contains(out, "nixpkgs") {
		t.Errorf("tampered narHash: exit %d, want 4 with BOB-E-3001 naming nixpkgs:\n%s", b.Exit, out)
	}
}

// TestRNIX003StandardStorePathsAndCaches is the gen-b round trip: sign, nix copy --to file://,
// copy into a fresh store, nix store verify --sigs, and run //cmd/api:api from there.
func TestRNIX003StandardStorePathsAndCaches(t *testing.T) {
	parity.Covers(t, "R-NIX-003")
	e := newBobEnv(t)
	r := e.run(t, "polyglot", "build", "--json", "--print-paths", "//cmd/api:api")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob build exit %d:\n%s", r.Exit, r.Combined())
	}
	var outPath string
	for _, ev := range events(t, r.Stdout) {
		if ev["event"] == "package.finish" && ev["label"] == "//cmd/api:api" {
			if ps, _ := ev["paths"].([]any); len(ps) > 0 {
				outPath = "/nix/store/" + filepath.Base(ps[0].(string))
			}
		}
	}
	if outPath == "" {
		t.Fatalf("no paths[] in the package.finish event of //cmd/api:api (--print-paths):\n%s", r.Stdout)
	}
	work := t.TempDir()
	key, pub := filepath.Join(work, "sec"), filepath.Join(work, "pub")
	if b, err := exec.Command("nix-store", "--generate-binary-cache-key", "bobtest-1", key, pub).CombinedOutput(); err != nil {
		t.Fatalf("harness: %v: %s", err, b)
	}
	pubKey := strings.TrimSpace(string(mustRead(t, pub)))
	if _, err := e.hostNix(t, nil, "store", "sign", "--recursive", "--key-file", key, outPath); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(work, "cache")
	if _, err := e.hostNix(t, nil, "copy", "--to", "file://"+cache, outPath); err != nil {
		t.Fatal(err)
	}
	fresh := &oracle{t: t, root: filepath.Join(work, "fresh"), home: t.TempDir()}
	opts := []string{"--option", "trusted-public-keys", pubKey, "--option", "substituters", "", "--option", "require-sigs", "true"}
	fresh.run(append([]string{"copy", "--from", "file://" + cache}, append(opts, outPath)...)...)
	fresh.run(append([]string{"store", "verify", "--sigs-needed", "1", "--recursive"}, append(opts, outPath)...)...)
	// Run it from the fresh store: a private mount namespace shows the fresh store as /nix.
	sum := sha256.Sum256([]byte("bob"))
	cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", "sh", "-c",
		"mount --bind "+filepath.Join(work, "fresh", "nix")+" /nix && exec "+outPath+"/bin/api")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running api from the fresh store: %v: %s", err, out)
	}
	// The line SPEC §01 R-FW-013 gives for //cmd/api:api (the digest is sha256("bob")).
	if want := "digest=" + hex.EncodeToString(sum[:]) + " numhex=3432 proto_bytes=116"; !strings.Contains(string(out), want) {
		t.Errorf("api output = %q, want a line containing %q", out, want)
	}
}

// TestRNIX002NixpkgsStaysUsable: examples/polyglot built against nixos-25.11 (b6018f87) after
// lowering the Go version: no builder change; only real version constraints fail, with 1008/2006.
func TestRNIX002NixpkgsStaysUsable(t *testing.T) {
	parity.Covers(t, "R-NIX-002")
	sha := fullRev(t, "b6018f87")
	e := newBobEnv(t)
	dir := e.dir(t, "polyglot")
	bobgo := filepath.Join(dir, "bob.go")
	s := string(mustRead(t, bobgo))
	edited := strings.Replace(s, `Nixpkgs: "nixos-26.05"`, `Nixpkgs: "nixos-25.11@`+sha+`"`, 1)
	edited = strings.Replace(edited, `Go:   "1.26"`, `Go:   "1.25"`, 1)
	if strings.Count(edited, "nixos-25.11@") != 1 || !strings.Contains(edited, `Go:   "1.25"`) {
		t.Fatal("harness: the polyglot fixture no longer has the Nixpkgs and Go lines this test edits")
	}
	mustWrite(t, bobgo, []byte(edited), 0o644)
	r := e.run(t, "polyglot", "build", "--json", "//...")
	if harness.Red(t, r) {
		return
	}
	nErrors := 0
	for _, ev := range events(t, r.Stdout) {
		if ev["event"] != "error" {
			continue
		}
		nErrors++
		em, _ := ev["error"].(map[string]any)
		if code, _ := em["code"].(string); code != "BOB-E-1008" && code != "BOB-E-2006" {
			t.Errorf("against nixos-25.11 only version constraints (1008/2006) may fail, got %v", em)
		}
	}
	// A failure without any error event (a crash, a bare message) would pass the loop above.
	if r.Exit != 0 && nErrors == 0 {
		t.Errorf("bob build exited %d against nixos-25.11 without an error event:\n%s", r.Exit, r.Combined())
	}
}
