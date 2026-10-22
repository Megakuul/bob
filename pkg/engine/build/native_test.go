package build

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// nat is a test ecosystem (kinds nat.*) with generated files: per node "<dir>/nat.txt" with
// the node's spec "text" (not committed) and "<dir>/nat.mod" (committed). Its lock section is
// fresh when it is {"inputs":"<sorted texts>"}.
type nat struct{}

func init() { engine.Register(nat{}) }

func (nat) Name() string { return "nat" }
func (nat) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (nat) Plan(context.Context, engine.PlanRequest) (engine.PlanResult, error) {
	return engine.PlanResult{}, nil
}

func natText(n *ir.Node) string {
	var s struct{ Text string }
	_ = json.Unmarshal(n.Spec, &s)
	return s.Text
}

func (nat) Native(_ context.Context, req engine.PlanRequest) ([]engine.GeneratedFile, error) {
	var out []engine.GeneratedFile
	for _, n := range req.Nodes {
		dir := strings.TrimPrefix(strings.SplitN(n.Label, ":", 2)[0], "//")
		text := natText(n)
		out = append(out,
			engine.GeneratedFile{Path: dir + "/nat.txt", Content: []byte(text + "\n"), Owner: n.Label,
				Explain: func(onDisk []byte) []engine.DriftLine {
					return []engine.DriftLine{{Op: "~", Text: text + " → " + strings.TrimSpace(string(onDisk)) + "  (set it in bob.go)"}}
				}},
			engine.GeneratedFile{Path: dir + "/nat.mod", Content: []byte("mod " + n.Label + "\n"), Owner: n.Label, Committed: true})
	}
	return out, nil
}

func (nat) Stale(_ context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	var texts []string
	var at ir.Pos
	for _, n := range req.Graph.Nodes {
		if strings.HasPrefix(n.Kind, "nat.") {
			texts = append(texts, natText(n))
			at = n.DefinedAt
		}
	}
	want := `{"inputs":"` + strings.Join(texts, ",") + `"}`
	if string(req.Prev) == want {
		return "", ir.Pos{}, nil
	}
	return "nat declarations changed", at, nil
}

func natGraph(texts ...string) *ir.Graph {
	g := &ir.Graph{IRVersion: ir.Version}
	for i, t := range texts {
		dir := string(rune('a' + i))
		spec, _ := json.Marshal(map[string]string{"text": t})
		g.Nodes = append(g.Nodes, &ir.Node{ID: dir, Kind: "nat.thing", Label: "//" + dir + ":" + dir,
			DefinedAt: ir.Pos{File: dir + "/bob.go", Line: 3, Col: 9}, Spec: spec})
	}
	return g
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codes(err error) []diag.Code {
	errs, _ := diagErrors(err)
	var out []diag.Code
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

// R-FW-060/R-FW-061 at engine level: files are written 0444 and recorded; a hand edit is drift
// (1011, nothing written) unless force, which backs the edit up; missing files and files bob
// wrote from older declarations are rewritten; orphans are removed.
func TestGenerateNativeFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	lf := &lock.File{LockVersion: lock.Version}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	excl, err := generate(ctx, root, natGraph("one", "two"), lf, nil, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(excl, ",") != "a/nat.txt,b/nat.txt" {
		t.Errorf("excluded = %v, want the not-committed files", excl)
	}
	txt := filepath.Join(root, "a", "nat.txt")
	if got := read(t, txt); got != "one\n" {
		t.Errorf("a/nat.txt = %q", got)
	}
	if fi, _ := os.Stat(txt); fi.Mode().Perm() != 0o444 {
		t.Errorf("mode %v, want 0444", fi.Mode().Perm())
	}
	var st nativeState
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, ".bob", "state", "native.json"))), &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != 1 || len(st.Files) != 4 || st.Files["a/nat.txt"].Owner != "//a:a" ||
		st.Files["a/nat.txt"].SHA256 != "2c8b08da5ce60398e1f19af0e5dccc744df274b826abe585eaba68c525434806" {
		t.Errorf("native.json = %+v", st)
	}

	// Hand edit: drift, nothing written.
	if err := os.Chmod(txt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(txt, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "b", "nat.txt")); err != nil {
		t.Fatal(err)
	}
	_, err = generate(ctx, root, natGraph("one", "two"), lf, nil, false, now)
	errs, _ := diagErrors(err)
	if len(errs) != 1 || errs[0].Code != 1011 || errs[0].Args["file"] != "a/nat.txt" ||
		len(errs[0].Excerpt) != 1 || errs[0].Excerpt[0] != "~ one → edited  (set it in bob.go)" {
		t.Fatalf("drift: %v (%+v)", err, errs)
	}
	if read(t, txt) != "edited\n" {
		t.Errorf("drift: the edited file was changed")
	}
	if _, err := os.Stat(filepath.Join(root, "b", "nat.txt")); err == nil {
		t.Errorf("drift: other files were written")
	}
	findings, err := checkNative(ctx, root, natGraph("one", "two"), lf)
	if err != nil || len(findings) != 1 || findings[0].Code != 1011 {
		t.Errorf("check: %v %v", findings, err)
	}

	// --force: backup and rewrite; the missing file is written too.
	if _, err := generate(ctx, root, natGraph("one", "two"), lf, nil, true, now); err != nil {
		t.Fatal(err)
	}
	if read(t, txt) != "one\n" || read(t, filepath.Join(root, "b", "nat.txt")) != "two\n" {
		t.Errorf("force did not restore the files")
	}
	if got := read(t, filepath.Join(root, ".bob", "gen", "backup", "20261022T120000Z", "a", "nat.txt")); got != "edited\n" {
		t.Errorf("backup = %q", got)
	}

	// Declarations changed: the copy bob wrote is rewritten, no drift; //b:b gone → orphans removed.
	if _, err := generate(ctx, root, natGraph("uno"), lf, nil, false, now); err != nil {
		t.Fatalf("changed declaration reported as drift: %v", err)
	}
	if read(t, txt) != "uno\n" {
		t.Errorf("a/nat.txt not rewritten")
	}
	if _, err := os.Stat(filepath.Join(root, "b", "nat.txt")); err == nil {
		t.Errorf("orphan b/nat.txt not removed")
	}

	// A directory where a file belongs: 1016, also with force.
	if err := os.Remove(txt); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(txt, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		if _, err := generate(ctx, root, natGraph("uno"), lf, nil, force, now); !errors.Is(err, err) || len(codes(err)) != 1 || codes(err)[0] != 1016 {
			t.Errorf("force=%v: directory at a generated path: %v", force, err)
		}
	}
}

// checkNative is the native half of Check on a given graph (no evaluation).
func checkNative(ctx context.Context, root string, g *ir.Graph, lf *lock.File) ([]*diag.Error, error) {
	files, err := nativeFiles(ctx, g, lf, nil)
	if err != nil {
		return nil, err
	}
	drift, err := compareNative(root, files, readNativeState(root))
	var out []*diag.Error
	for _, d := range drift {
		out = append(out, d.finding())
	}
	return out, err
}

// bob check's lock findings (1012): missing nixpkgs pin, stale ecosystem section with its
// position, unused section and unused source entry.
func TestLockFindings(t *testing.T) {
	ctx := context.Background()
	g := natGraph("one")
	g.Nodes = append(g.Nodes, &ir.Node{ID: "nixpkgs:zlib", Kind: "nixpkgs.lib", Label: "nixpkgs:zlib", Nix: &ir.Nix{Attr: "zlib"}})
	lf := &lock.File{LockVersion: lock.Version,
		Sections: map[string]json.RawMessage{"nat": json.RawMessage(`{"inputs":"old"}`), "go": json.RawMessage(`{}`)},
		Sources:  map[string]lock.Source{"//x:dl": {URL: "https://example.invalid/x"}}}
	got, err := lockFindings(ctx, g, lf, offlineFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	var whats []string
	for _, d := range got {
		if d.Code != 1012 {
			t.Errorf("code %d, want 1012", d.Code)
		}
		whats = append(whats, d.Args["what"])
	}
	want := []string{"nixpkgs is not pinned", "//x:dl is pinned but no longer declared", "the go section is no longer used", "nat declarations changed"}
	if strings.Join(whats, "|") != strings.Join(want, "|") {
		t.Errorf("findings %q, want %q", whats, want)
	}
	if last := got[len(got)-1]; last.DefinedAt.File != "a/bob.go" || last.DefinedAt.Line != 3 {
		t.Errorf("stale section position %+v", last.DefinedAt)
	}

	lf = &lock.File{LockVersion: lock.Version, Sections: map[string]json.RawMessage{"nat": json.RawMessage(`{"inputs":"one"}`)}}
	lf.Nixpkgs = lock.NixpkgsPin{Channel: lock.DefaultChannel, Rev: "0123456789abcdef0123456789abcdef01234567"}
	if got, err := lockFindings(ctx, g, lf, offlineFetcher{}); err != nil || len(got) != 0 {
		t.Errorf("fresh lock: %v %v", got, err)
	}
}

func TestNativePathOK(t *testing.T) {
	for p, ok := range map[string]bool{"go.mod": true, "a/b/Cargo.toml": true, ".cargo/config.toml": true,
		".bob/vite.bob.config.mjs": true, "": false, "../x": false, "/etc/passwd": false, "a/../b": false,
		".git/config": false, ".bob/state/native.json": false, "a\\b": false} {
		if nativePathOK(p) != ok {
			t.Errorf("nativePathOK(%q) = %v", p, !ok)
		}
	}
}

// Package.Drv (bob log of a package) is its derivation; Check and Gen of a demo repo without
// generated files are clean and write nothing but native.json; CheckDeterminism passes.
func TestRunDrvCheckGen(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates and builds in a real store")
	}
	st := testStore(t)
	dir := demoRepo(t)
	res, _ := runBuild(t, Options{Root: dir, Store: st})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d:\n%s", res.ExitCode, describe(res))
	}
	for _, p := range res.Packages {
		if !strings.HasPrefix(p.Drv, "/nix/store/") || !strings.HasSuffix(p.Drv, ".drv") {
			t.Errorf("%s: Drv %q", p.Label, p.Drv)
		}
	}
	ctx := context.Background()
	o := Options{Root: dir, Store: st}
	findings, err := Check(ctx, o)
	if err != nil || len(findings) != 0 {
		t.Errorf("Check: %v %v", findings, err)
	}
	if excl, err := Gen(ctx, o, false); err != nil || len(excl) != 0 {
		t.Errorf("Gen: %v %v", excl, err)
	}
	if err := CheckDeterminism(ctx, o); err != nil {
		t.Errorf("CheckDeterminism: %v", err)
	}
	// A stale lock entry: a pinned source nothing declares.
	lf := `{"lockVersion": 1, "sources": {"//x:gone": {"kind": "file", "url": "https://example.invalid/x", "name": "x", "hash": "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="}}}`
	if err := os.WriteFile(filepath.Join(dir, "bob.lock"), []byte(lf+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err = Check(ctx, o)
	if err != nil || len(findings) != 1 || findings[0].Code != 1012 {
		t.Errorf("stale lock: %v %v", findings, err)
	}
}
