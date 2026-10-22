package rust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	bobrust "github.com/megakuul/bob/pkg/rust"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// workspaceSpec is one scratch workspace of the corpus: a single library crate "w" with the
// given crates.io requirements (name, requirement, features).
type workspaceSpec struct {
	name string
	deps [][3]string
}

// corpusWorkspaces are real-world dependency sets of increasing size. The four S3 graphs the
// SPEC names (docs/spikes/s03-rust.md 3.2: serde_json 12, tokio full 25, web 173, big 393
// packages) come first, spelled from the spike's table; the spike's exact Cargo.toml files are
// not in the repository, so their package counts are as stated there only approximately (see
// docs/review/owner-items.md, FIX-C). The remaining sets are author-chosen.
var corpusWorkspaces = []workspaceSpec{
	{"s3-serde_json", [][3]string{{"serde_json", "1", ""}}},
	{"s3-tokio-full", [][3]string{{"tokio", "1", "full"}}},
	{"s3-web", [][3]string{{"tokio", "1", "full"}, {"axum", "0.7", ""}, {"reqwest", "0.12", "rustls-tls"}, {"tracing", "0.1", ""}, {"serde", "1", "derive"}, {"clap", "4", "derive"}, {"anyhow", "1", ""}}},
	{"s3-big", [][3]string{{"tokio", "1", "full"}, {"axum", "0.7", ""}, {"reqwest", "0.12", "rustls-tls"}, {"tracing", "0.1", ""}, {"serde", "1", "derive"}, {"clap", "4", "derive"}, {"anyhow", "1", ""},
		{"sqlx", "0.8", "runtime-tokio,postgres,sqlite"}, {"tonic", "0.12", ""}, {"prost", "0.13", ""}, {"opentelemetry", "0.27", ""}, {"chrono", "0.4", ""}, {"uuid", "1", "v4"}, {"rand", "0.8", ""}, {"image", "0.25", ""}}},
	{"itoa-hex", [][3]string{{"itoa", "1", ""}, {"hex", "0.4", ""}}},
	{"serde", [][3]string{{"serde", "1", "derive"}, {"serde_json", "1", ""}}},
	{"regex", [][3]string{{"regex", "1", ""}}},
	{"clap", [][3]string{{"clap", "4", "derive"}}},
	{"anyhow-thiserror", [][3]string{{"anyhow", "1", ""}, {"thiserror", "2", ""}}},
	{"tokio", [][3]string{{"tokio", "1", "rt,macros,time"}}},
	{"zstd", [][3]string{{"zstd", "0.13", ""}}},
	{"reqwest", [][3]string{{"reqwest", "0.12", "json"}}},
	{"axum", [][3]string{{"axum", "0.7", ""}, {"tower", "0.5", ""}}},
	{"tonic", [][3]string{{"tonic", "0.12", ""}, {"prost", "0.13", ""}}},
}

// corpusRepo writes a repository with the crate "w" requiring spec.deps.
func corpusRepo(t *testing.T, spec workspaceSpec) string {
	t.Helper()
	var deps strings.Builder
	for _, d := range spec.deps {
		if d[2] == "" {
			fmt.Fprintf(&deps, "\t\trust.Crate(%q, %q),\n", d[0], d[1])
			continue
		}
		var fs []string
		for _, f := range strings.Split(d[2], ",") {
			fs = append(fs, fmt.Sprintf("%q", f))
		}
		fmt.Fprintf(&deps, "\t\trust.Crate(%q, %q, rust.Features(%s)),\n", d[0], d[1], strings.Join(fs, ", "))
	}
	files := map[string]string{
		"go.mod":       "module example.com/rsroot\n\ngo 1.26.0\n",
		"bob.go":       "//go:build bob\n\npackage rsroot\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"w/src/lib.rs": "pub fn id() -> u32 { 1 }\n",
		"w/bob.go": "//go:build bob\n\npackage w\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/rust\"\n)\n\n" +
			"// Lib is the corpus crate (label //w:w).\nvar Lib = bob.Define(func(p *bob.P) *rust.Lib {\n\treturn rust.Library(p, rust.LibOpts{Deps: []rust.Dep{\n" + deps.String() + "\t}})\n})\n",
	}
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "corpus"},
	} {
		gitIn(t, dir, args...)
	}
	return dir
}

// cargoLockfile runs the pinned cargo's generate-lockfile in a copy of the workspace's
// generated Cargo.toml files and sources, with crates.io replaced by the registry mirror
// (the same index snapshot bob resolved against), and returns the Cargo.lock.
func cargoLockfile(t *testing.T, dir string) string {
	t.Helper()
	mirror := os.Getenv("BOB_TEST_CRATES_INDEX")
	if mirror == "" {
		t.Fatalf("harness: BOB_TEST_CRATES_INDEX (sparse index URL of tools/regmirror) is required to run cargo against the same index snapshot as bob")
	}
	work := filepath.Join(t.TempDir(), "ws")
	err := copyFiltered(dir, work)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	cfg := "[source.crates-io]\nreplace-with = \"mirror\"\n[source.mirror]\nregistry = \"sparse+" + strings.TrimSuffix(mirror, "/") + "/\"\n"
	if err := os.MkdirAll(filepath.Join(work, ".cargo"), 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, ".cargo", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("harness: %v", err)
	}
	cargo := filepath.Join(pinnedTool(t, "cargo"), "bin", "cargo")
	cmd := exec.Command(cargo, "generate-lockfile")
	cmd.Dir = work
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "CARGO_HOME=" + t.TempDir(), "CARGO_NET_OFFLINE=false"}
	if r := runCmd(t, cmd, []string{"cargo", "generate-lockfile"}); r.Exit != 0 {
		t.Fatalf("cargo generate-lockfile exited %d:\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	return readFile(t, filepath.Join(work, "Cargo.lock"))
}

// copyFiltered copies dir to dst without .git, .bob, .cargo, Cargo.lock and target.
func copyFiltered(dir, dst string) error {
	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case rel == ".":
			return os.MkdirAll(dst, 0o755)
		case info.IsDir() && (rel == ".git" || rel == ".bob" || rel == ".cargo" || rel == "target"):
			return filepath.SkipDir
		case rel == "Cargo.lock":
			return nil
		case info.IsDir():
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		}
		return nil
	})
}

// R-RS-002a (resolver): the Cargo.lock bob generates is byte-identical to `cargo
// generate-lockfile` of the pinned cargo on the same index snapshot, on the Rust fixture and
// a corpus of further workspaces (>= 10 in total).
func TestRRS002aResolutionEqualsCargo(t *testing.T) {
	parity.Covers(t, "R-RS-002a")
	type entry struct {
		name string
		repo func(t *testing.T) string
	}
	entries := []entry{{"fixture-rust", func(t *testing.T) string { return harness.Fixture(t, "rust") }}}
	for _, w := range corpusWorkspaces {
		w := w
		entries = append(entries, entry{w.name, func(t *testing.T) string { return corpusRepo(t, w) }})
	}
	if len(entries) < 10 {
		t.Fatalf("harness: the corpus has %d workspaces, SPEC asks for at least 10", len(entries))
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			home := t.TempDir()
			dir := e.repo(t)
			if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
				return
			}
			if r := bobEnv(t, dir, registryEnv(home), "gen"); !ok(t, r) {
				return
			}
			ours := readFile(t, filepath.Join(dir, "Cargo.lock"))
			if want := cargoLockfile(t, dir); ours != want {
				t.Errorf("bob's Cargo.lock differs from cargo generate-lockfile (%d vs %d bytes)\n%s", len(ours), len(want), firstDiffLine(ours, want))
			}
		})
	}
}

func firstDiffLine(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("first difference at line %d:\n  bob:   %s\n  cargo: %s", i+1, al[i], bl[i])
		}
	}
	return fmt.Sprintf("one file is a prefix of the other (%d vs %d lines)", len(al), len(bl))
}

// R-RS-002a (cfg tables): bob's built-in cfg tables equal `rustc --print cfg --target <t>` of
// the pinned toolchain for both Linux targets.
func TestRRS002aCfgTablesEqualRustc(t *testing.T) {
	parity.Covers(t, "R-RS-002a")
	targets := []string{"aarch64-unknown-linux-gnu", "x86_64-unknown-linux-gnu"}
	tables := map[string][]string{}
	for _, tg := range targets {
		tab, err := bobrust.CfgTable(tg)
		var d *diag.Error
		if errors.As(err, &d) && d.Code == diag.CodeNotImplemented {
			harness.RedFeature(t, "rust.CfgTable")
			return
		}
		if err != nil {
			t.Errorf("rust.CfgTable(%s): %v", tg, err)
			continue
		}
		tables[tg] = tab
	}
	rustc := filepath.Join(pinnedTool(t, "rustc"), "bin", "rustc")
	for tg, tab := range tables {
		cmd := exec.Command(rustc, "--print", "cfg", "--target", tg)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
		r := runCmd(t, cmd, []string{"rustc", "--print", "cfg", "--target", tg})
		if !exitIs(t, r, 0) {
			continue
		}
		wantLines := splitLines(r.Stdout)
		got := append([]string(nil), tab...)
		sort.Strings(got)
		sort.Strings(wantLines)
		if strings.Join(got, "\n") != strings.Join(wantLines, "\n") {
			t.Errorf("cfg table of %s differs from rustc --print cfg\nbob:\n%s\nrustc:\n%s", tg, strings.Join(got, "\n"), strings.Join(wantLines, "\n"))
		}
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// lockedCrates returns the crate entries of the rust section of bob.lock.
func lockedCrates(t *testing.T, dir string) []map[string]any {
	t.Helper()
	var l struct {
		Rust struct {
			Crates []map[string]any `json:"crates"`
		} `json:"rust"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "bob.lock"))), &l); err != nil {
		t.Fatalf("bob.lock: %v", err)
	}
	return l.Rust.Crates
}
