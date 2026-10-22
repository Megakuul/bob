package resolve

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// cmpWorkspace is one workspace of the cargo comparison corpus: a single library member with
// the given crates.io dependencies (name, requirement, comma-separated features).
type cmpWorkspace struct {
	name string
	deps [][3]string
}

// cmpCorpus holds the four S3 graphs as the acceptance corpus spells them
// (tests/eco/rust/resolve_test.go), the S3 spike's own big graph, and smaller sets.
var cmpCorpus = []cmpWorkspace{
	{"s3-serde_json", [][3]string{{"serde_json", "1", ""}}},
	{"s3-tokio-full", [][3]string{{"tokio", "1", "full"}}},
	{"s3-web", [][3]string{{"tokio", "1", "full"}, {"axum", "0.7", ""}, {"reqwest", "0.12", "rustls-tls"}, {"tracing", "0.1", ""}, {"serde", "1", "derive"}, {"clap", "4", "derive"}, {"anyhow", "1", ""}}},
	{"s3-big", [][3]string{{"tokio", "1", "full"}, {"axum", "0.7", ""}, {"reqwest", "0.12", "rustls-tls"}, {"tracing", "0.1", ""}, {"serde", "1", "derive"}, {"clap", "4", "derive"}, {"anyhow", "1", ""},
		{"sqlx", "0.8", "runtime-tokio,postgres,sqlite"}, {"tonic", "0.12", ""}, {"prost", "0.13", ""}, {"opentelemetry", "0.27", ""}, {"chrono", "0.4", ""}, {"uuid", "1", "v4"}, {"rand", "0.8", ""}, {"image", "0.25", ""}}},
	{"spike-big", [][3]string{{"tokio", "1", "full"}, {"axum", "0.8", ""}, {"reqwest", "0.12", "json,rustls-tls"}, {"sqlx", "0.8", "runtime-tokio,postgres,sqlite"},
		{"tonic", "0.13", ""}, {"prost", "0.13", ""}, {"tracing-subscriber", "0.3", "env-filter,json"}, {"opentelemetry", "0.29", ""}, {"chrono", "0.4", "serde"},
		{"uuid", "1", "v4,serde"}, {"rand", "0.8", ""}, {"image", "0.25", ""}, {"clap", "4", "derive"}}},
	{"zstd", [][3]string{{"zstd", "0.13", ""}}},
	{"regex", [][3]string{{"regex", "1", ""}}},
	{"openssl", [][3]string{{"openssl", "0.10", ""}}},
	{"wasm-bindgen", [][3]string{{"wasm-bindgen", "0.2", ""}, {"getrandom", "0.2", "js"}}},
}

func (w cmpWorkspace) member() (Member, error) {
	m := Member{Name: "w", Version: "0.1.0"}
	ds := append([][3]string(nil), w.deps...)
	sort.Slice(ds, func(i, j int) bool { return ds[i][0] < ds[j][0] }) // Cargo.toml tables are sorted maps
	for _, d := range ds {
		req, err := ParseReq(d[1])
		if err != nil {
			return Member{}, err
		}
		var fs []string
		if d[2] != "" {
			fs = strings.Split(d[2], ",")
		}
		m.Deps = append(m.Deps, Dep{Name: d[0], Package: d[0], Req: req, Default: true, Features: fs})
	}
	return m, nil
}

func (w cmpWorkspace) write(t *testing.T, dir, resolver string) {
	t.Helper()
	var deps strings.Builder
	for _, d := range w.deps {
		fs := ""
		if d[2] != "" {
			fs = `, features = ["` + strings.Join(strings.Split(d[2], ","), `", "`) + `"]`
		}
		fmt.Fprintf(&deps, "%s = { version = %q%s }\n", d[0], d[1], fs)
	}
	files := map[string]string{
		"Cargo.toml":     "[workspace]\nresolver = \"" + resolver + "\"\nmembers = [\"w\"]\n",
		"w/Cargo.toml":   "[package]\nname = \"w\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\n" + deps.String(),
		"w/src/lib.rs":   "",
		".cargo/.keep":   "",
		"w/.cargo/.keep": "",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCompareWithCargo resolves the corpus with bob and with `cargo generate-lockfile` on one
// live index snapshot and requires byte-identical Cargo.lock files. It needs the network
// (crates.io) and a cargo binary: BOB_TEST_CARGO=<path to cargo>, optionally
// BOB_TEST_RUSTC=<dir with rustc> for the resolver-3 (rust-version aware) runs and
// BOB_TEST_SNAPSHOT=<dir> to keep the index files used.
func TestCompareWithCargo(t *testing.T) {
	cargo := os.Getenv("BOB_TEST_CARGO")
	if cargo == "" {
		t.Skip("BOB_TEST_CARGO is not set (online comparison with cargo generate-lockfile)")
	}
	snap := os.Getenv("BOB_TEST_SNAPSHOT")
	if snap == "" {
		snap = t.TempDir()
	}
	upstream := NewIndex(CratesIO, snap)
	// The mirror serves cargo the very files bob resolved against, fetching what bob never
	// needed through the same client, so both see one snapshot.
	url := serveIndex(t, func(ctx context.Context, rel string) ([]byte, error) { return upstream.fetch(ctx, rel) })
	ix := NewIndex(url, "")
	for _, w := range cmpCorpus {
		for _, rn := range cargoRuns(t) {
			t.Run(w.name+"/resolver"+rn.resolver, func(t *testing.T) {
				start := time.Now()
				ours := bobCargoLock(t, ix, w, rn.rustVersion)
				took := time.Since(start)
				want := cargoGenerate(t, cargo, url, w, rn.resolver)
				t.Logf("%d packages, bob resolve %v", strings.Count(ours, "[[package]]"), took)
				if ours != want {
					t.Errorf("Cargo.lock differs from cargo's\n%s", diffLines(ours, want))
				}
			})
		}
	}
}

// snapshotWorkspaces are resolved offline from testdata/index.tar.gz (recorded crates.io
// index files) and compared with the Cargo.lock files cargo 1.95.0 generated from the same
// files (testdata/<name>.resolver<N>.Cargo.lock).
var snapshotWorkspaces = []string{"s3-serde_json", "s3-tokio-full"}

// TestSnapshotEqualsCargo is the offline regression test of the resolver against Cargo. With
// BOB_TEST_CARGO set it also runs cargo on the snapshot (BOB_TEST_RECORD=1 rewrites the
// expected files from cargo's output).
func TestSnapshotEqualsCargo(t *testing.T) {
	files := readSnapshot(t, filepath.Join("testdata", "index.tar.gz"))
	reg := MemRegistry{}
	for rel, body := range files {
		name := filepath.Base(rel)
		es, err := ParseIndexFile(name, body)
		if err != nil {
			t.Fatal(err)
		}
		reg[name] = es
	}
	cargo := os.Getenv("BOB_TEST_CARGO")
	var url string
	if cargo != "" {
		url = serveIndex(t, func(_ context.Context, rel string) ([]byte, error) {
			if b, ok := files[rel]; ok {
				return b, nil
			}
			return nil, ErrNoCrate
		})
	}
	for _, name := range snapshotWorkspaces {
		var w cmpWorkspace
		for _, c := range cmpCorpus {
			if c.name == name {
				w = c
			}
		}
		for _, rn := range []cargoRun{{"2", ""}, {"3", "1.95.0"}} {
			t.Run(name+"/resolver"+rn.resolver, func(t *testing.T) {
				golden := filepath.Join("testdata", name+".resolver"+rn.resolver+".Cargo.lock")
				if cargo != "" {
					got := cargoGenerate(t, cargo, url, w, rn.resolver)
					if os.Getenv("BOB_TEST_RECORD") == "1" {
						if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
							t.Fatal(err)
						}
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if cargo != "" {
					if got := cargoGenerate(t, cargo, url, w, rn.resolver); got != string(want) {
						t.Fatalf("cargo no longer produces %s:\n%s", golden, diffLines(got, string(want)))
					}
				}
				if ours := bobCargoLock(t, reg, w, rn.rustVersion); ours != string(want) {
					t.Errorf("Cargo.lock differs from %s\n%s", golden, diffLines(ours, string(want)))
				}
			})
		}
	}
}

type cargoRun struct{ resolver, rustVersion string }

func cargoRuns(t *testing.T) []cargoRun {
	runs := []cargoRun{{"2", ""}}
	if rc := os.Getenv("BOB_TEST_RUSTC"); rc != "" {
		runs = append(runs, cargoRun{"3", rustcVersion(t, rc)})
	}
	return runs
}

func bobCargoLock(t *testing.T, reg Registry, w cmpWorkspace, rustVersion string) string {
	t.Helper()
	m, err := w.member()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(context.Background(), reg, []Member{m}, Options{RustVersion: rustVersion})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := res.CargoLock()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// serveIndex serves a sparse index whose files come from get.
func serveIndex(t *testing.T, get func(ctx context.Context, rel string) ([]byte, error)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if rel == "config.json" {
			_, _ = w.Write([]byte(`{"dl":"https://static.crates.io/crates"}`))
			return
		}
		body, err := get(r.Context(), rel)
		if errors.Is(err, ErrNoCrate) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// cargoGenerate runs `cargo generate-lockfile` for w against the sparse index at url.
func cargoGenerate(t *testing.T, cargo, url string, w cmpWorkspace, resolver string) string {
	t.Helper()
	dir := t.TempDir()
	w.write(t, dir, resolver)
	cfg := "[source.crates-io]\nreplace-with = \"mirror\"\n[source.mirror]\nregistry = \"sparse+" + url + "/\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargo, "generate-lockfile")
	cmd.Dir = dir
	path := "/usr/bin:/bin"
	if rc := os.Getenv("BOB_TEST_RUSTC"); rc != "" {
		path = rc + ":" + path
	}
	cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir(), "CARGO_HOME=" + t.TempDir()}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo generate-lockfile: %v\n%s", err, out)
	}
	want, err := os.ReadFile(filepath.Join(dir, "Cargo.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return string(want)
}

// readSnapshot returns the files of a gzip tar (path relative to the index root -> content).
func readSnapshot(t *testing.T, file string) map[string][]byte {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, MaxIndexFile))
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimPrefix(h.Name, "./")] = b
	}
}

func rustcVersion(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(dir, "rustc"), "--version").Output()
	if err != nil {
		t.Fatalf("rustc --version: %v", err)
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		t.Fatalf("rustc --version: %q", out)
	}
	return f[1]
}

func diffLines(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, l := range al {
		inA[l] = true
	}
	for _, l := range bl {
		inB[l] = true
	}
	var out []string
	for _, l := range al {
		if !inB[l] {
			out = append(out, "bob:   "+l)
		}
	}
	for _, l := range bl {
		if !inA[l] {
			out = append(out, "cargo: "+l)
		}
	}
	if len(out) == 0 {
		return "same lines, different order"
	}
	if len(out) > 40 {
		out = append(out[:40], "...")
	}
	return strings.Join(out, "\n")
}

// TestUnifyEqualsCargoTree compares evaluation-time features (Unify, native aarch64 build of
// the member's library) with `cargo tree -e normal,build -f '{p} {f}'` on the corpus: the set
// of (crate, enabled features) pairs must be equal. Online, like TestCompareWithCargo; it
// downloads every crate for its Cargo.toml facts (cached in BOB_TEST_CRATES, default a
// temporary directory).
func TestUnifyEqualsCargoTree(t *testing.T) {
	cargo := os.Getenv("BOB_TEST_CARGO")
	if cargo == "" {
		t.Skip("BOB_TEST_CARGO is not set (online comparison with cargo tree)")
	}
	upstream := NewIndex(CratesIO, t.TempDir())
	url := serveIndex(t, func(ctx context.Context, rel string) ([]byte, error) { return upstream.fetch(ctx, rel) })
	ix := NewIndex(url, "")
	crateDir := os.Getenv("BOB_TEST_CRATES")
	if crateDir == "" {
		crateDir = t.TempDir()
	}
	hostTriple := map[string]string{"arm64": "aarch64-unknown-linux-gnu", "amd64": "x86_64-unknown-linux-gnu"}[runtime.GOARCH]
	host, err := NewPlatform(hostTriple)
	if err != nil {
		t.Skipf("no cfg table for this host: %v", err)
	}
	for _, w := range cmpCorpus {
		for _, triple := range []string{"aarch64-unknown-linux-gnu", "x86_64-unknown-linux-gnu"} {
			plat, err := NewPlatform(triple)
			if err != nil {
				t.Fatal(err)
			}
			t.Run(w.name+"/"+triple, func(t *testing.T) {
				m, err := w.member()
				if err != nil {
					t.Fatal(err)
				}
				res, err := Resolve(context.Background(), ix, []Member{m}, Options{})
				if err != nil {
					t.Fatal(err)
				}
				recs, err := res.Records()
				if err != nil {
					t.Fatal(err)
				}
				for i := range recs {
					recs[i].Meta = crateMeta(t, crateDir, recs[i].Name, recs[i].Version)
				}
				units, err := Unify([]Member{m}, recs, []Root{{Member: "w", Default: true}}, host, plat)
				if err != nil {
					t.Fatal(err)
				}
				ours := map[string]bool{}
				for _, u := range units {
					name, ver, _ := strings.Cut(u.Key.Crate, "@")
					ours[name+" v"+ver+"|"+strings.Join(u.Features, ",")] = true
				}
				dir := t.TempDir()
				w.write(t, dir, "2")
				cfg := "[source.crates-io]\nreplace-with = \"mirror\"\n[source.mirror]\nregistry = \"sparse+" + url + "/\"\n"
				if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cfg), 0o644); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(cargo, "tree", "-e", "normal,build", "--target", plat.Triple, "--prefix", "none", "-f", "{p}|{f}")
				cmd.Dir = dir
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "CARGO_HOME=" + t.TempDir()}
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("cargo tree: %v", err)
				}
				theirs := map[string]bool{}
				for _, l := range strings.Split(string(out), "\n") {
					l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "(*)"))
					if l == "" {
						continue
					}
					p, f, _ := strings.Cut(l, "|")
					fields := strings.Fields(p) // "name v1.2.3 [(proc-macro)] [(/path)]"
					if len(fields) < 2 {
						continue
					}
					fs := strings.Split(f, ",")
					if f == "" {
						fs = nil
					}
					sort.Strings(fs)
					ver := fields[1]
					if fields[0] == "w" {
						ver = "v0.1.0"
					}
					theirs[fields[0]+" "+ver+"|"+strings.Join(fs, ",")] = true
				}
				for k := range ours {
					if !theirs[k] {
						t.Errorf("bob only:   %s", k)
					}
				}
				for k := range theirs {
					if !ours[k] {
						t.Errorf("cargo only: %s", k)
					}
				}
				t.Logf("%d units, %d (crate, features) pairs", len(units), len(ours))
			})
		}
	}
}

// crateMeta downloads a crate (cached in dir) and reads its Cargo.toml facts.
func crateMeta(t *testing.T, dir, name, version string) CrateMeta {
	t.Helper()
	f := filepath.Join(dir, name+"-"+version+".crate")
	data, err := os.ReadFile(f)
	if err != nil {
		resp, err := http.Get("https://static.crates.io/crates/" + name + "/" + name + "-" + version + ".crate")
		if err != nil {
			t.Fatal(err)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("download %s %s: %v %s", name, version, err, resp.Status)
		}
		if err := os.WriteFile(f, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := MetaFromCrate(name, version, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
