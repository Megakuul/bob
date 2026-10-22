package testrun

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/services/postgres"
)

type modResolver map[string]*drv.Derivation

func (m modResolver) Modulo(p string) ([32]byte, error) {
	d, ok := m[p]
	if !ok {
		return [32]byte{}, fmt.Errorf("unknown drv %s", p)
	}
	return d.Modulo(m)
}

// testJob is a finalised test-run derivation for label with the given args.
func testJob(t *testing.T, builder, label string, args map[string]any, srcs ...string) *drv.Derivation {
	t.Helper()
	spec := map[string]any{"v": 1, "kind": Kind, "label": label, "outputs": []any{"out"}, "args": args, "fixup": map[string]any{}}
	d, err := drv.Job{Name: "bob-test-" + strings.NewReplacer("/", "", ":", "-").Replace(label), System: "aarch64-linux",
		Builder: builder, Spec: spec, InputSrcs: append([]string{builder}, srcs...)}.Derivation()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Finalize(modResolver{}); err != nil {
		t.Fatal(err)
	}
	return d
}

type fakeStore map[string]bool

func (f fakeStore) Invalid(_ context.Context, paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		if !f[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

func TestUnitsCachedRerun(t *testing.T) {
	b := "/nix/store/kx7xfnlcaq7vb0hd90nn16d6v4k13k2b-bob-builder-1"
	a := testJob(t, b, "//b:go_test", map[string]any{"argv": []any{"t"}})
	c := testJob(t, b, "//a:go_test", map[string]any{"argv": []any{"t"}})
	run, err := drv.Job{Name: "step", System: "aarch64-linux", Builder: b, InputSrcs: []string{b},
		Spec: map[string]any{"v": 1, "kind": "run", "label": "//a:step", "args": map[string]any{"steps": []any{}}, "fixup": map[string]any{}}}.Derivation()
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Finalize(nil); err != nil {
		t.Fatal(err)
	}
	units, err := Units([]*drv.Derivation{a, run, c})
	if err != nil || len(units) != 2 || units[0].Label != "//a:go_test" || units[1].Label != "//b:go_test" {
		t.Fatalf("Units = %+v, %v", units, err)
	}
	if units[0].Out != c.Outputs[0].Path || !strings.HasSuffix(units[0].DrvPath, ".drv") {
		t.Errorf("unit %+v", units[0])
	}
	if _, err := Units([]*drv.Derivation{a, a}); err == nil {
		t.Errorf("two units with one label accepted")
	}

	got, err := Cached(context.Background(), fakeStore{units[1].Out: true}, units)
	if err != nil || len(got) != 1 || !got["//b:go_test"] {
		t.Errorf("Cached = %v, %v", got, err)
	}

	r1, err := Rerun(units[0], "n1", modResolver{})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := Rerun(units[0], "n2", modResolver{})
	if r1.Out == units[0].Out || r1.Out == r2.Out || r1.Label != units[0].Label {
		t.Errorf("rerun outputs %s %s %s", units[0].Out, r1.Out, r2.Out)
	}
	if _, ok := units[0].Drv.Structured[RerunAttribute]; ok || units[0].Drv.Outputs[0].Path != units[0].Out {
		t.Errorf("Rerun changed the original derivation")
	}
	if again, _ := Rerun(units[0], "n1", modResolver{}); again.Out != r1.Out {
		t.Errorf("rerun with the same nonce differs")
	}
	if _, err := Rerun(units[0], "", modResolver{}); err == nil {
		t.Errorf("empty nonce accepted")
	}
}

func TestResultDecoding(t *testing.T) {
	dir := t.TempDir()
	ok := `{"version":1,"label":"//a:t","passed":true,"durationSeconds":1.2344,"cases":[{"name":"TestA","result":"pass","durationSeconds":0.5}]}`
	os.WriteFile(filepath.Join(dir, "test-result.json"), []byte(ok), 0o644)
	r, err := ReadResult(dir)
	if err != nil || !r.Passed || r.DurationMs() != 1234 || len(r.Cases) != 1 {
		t.Errorf("ReadResult = %+v, %v", r, err)
	}
	for _, bad := range []string{
		`{"version":2,"label":"//a:t","passed":true,"durationSeconds":1,"cases":[]}`,
		`{"version":1,"label":"","passed":true,"durationSeconds":1,"cases":[]}`,
		`{"version":1,"label":"//a:t","passed":true,"durationSeconds":1,"cases":[],"extra":1}`,
		`{"version":1,"label":"//a:t","passed":true,"durationSeconds":1,"cases":[{"name":"x","result":"maybe"}]}`,
	} {
		if _, err := decodeResult([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	log := "=== RUN TestB\n--- FAIL: TestB\nbob:: {\"type\":\"diag\"}\nbob:: {\"type\":\"test-result\",\"result\":" +
		`{"version":1,"label":"//a:t","passed":false,"durationSeconds":0.1,"cases":[{"name":"TestB","result":"fail","durationSeconds":0,"output":"boom\n"}]}` +
		"}\nbob-builder: //a:t: test failed (exit status 1)\n"
	fr, found := FailedResult(strings.NewReader(log))
	if !found || fr.Passed || fr.Cases[0].Output != "boom\n" || len(fr.Failed()) != 1 || fr.Failed()[0] != "TestB" {
		t.Errorf("FailedResult = %+v %v", fr, found)
	}
	if _, found := FailedResult(strings.NewReader("bob-builder: //a:t: service x did not become ready within 30s\n")); found {
		t.Errorf("found a result in a log without one")
	}
}

// program is a small static test binary: "pass" passes, "listen <port>" serves on $ADDR,
// "dial <VAR> <port>" passes when $VAR:<port> accepts.
const program = `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	switch os.Args[1] {
	case "pass":
		fmt.Println("=== RUN   TestOK")
		fmt.Println("--- PASS: TestOK (0.00s)")
	case "listen":
		l, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("ADDR"), os.Args[2]))
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("listening on", l.Addr())
		for {
			c, err := l.Accept()
			if err == nil {
				c.Close()
			}
		}
	case "dial":
		c, err := net.DialTimeout("tcp", net.JoinHostPort(os.Getenv(os.Args[2]), os.Args[3]), time.Second)
		if err != nil {
			fmt.Println("--- FAIL: TestDial (0.00s)", err)
			os.Exit(1)
		}
		c.Close()
		fmt.Println("--- PASS: TestDial (0.00s)", os.Getenv(os.Args[2]))
	}
}
`

// importProgram compiles program and imports it as a store path holding bin/t.
func importProgram(t *testing.T, s *store.Store) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module t\n\ngo 1.24\n"), 0o644)
	tree := filepath.Join(t.TempDir(), "tree")
	cmd := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(tree, "bin", "t"), ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	paths, err := srcs.Import(context.Background(), s, []srcs.Source{{Name: "bob-testrun-prog", Tree: srcs.Tree{Root: tree, Files: []string{"bin/t"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return paths[0]
}

func realise(t *testing.T, s *store.Store, d *drv.Derivation, extra ...string) (string, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.ImportDrvs(ctx, []*drv.Derivation{d}); err != nil {
		t.Fatal(err)
	}
	p, _ := d.Path()
	var log bytes.Buffer
	c := s.Command(ctx, "nix-store", append([]string{"--realise", p, "--option", "substituters", ""}, extra...)...)
	c.Stderr = &log
	err := c.Run()
	return log.String(), err
}

// In the real Nix sandbox: a test unit with a loopback service on 127.0.0.2 passes; afterwards
// its result is cached (valid) and readable; Nix's --check (what --rebuild does) fails on the
// timings in the result (O-02-9), while the Rerun derivation runs the test again and passes.
func TestTestRunInNix(t *testing.T) {
	if testing.Short() {
		t.Skip("builds in the real store")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	builder, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prog := importProgram(t, s)
	salt := fmt.Sprint(time.Now().UnixNano())
	d := testJob(t, builder, "//it:it", map[string]any{
		"argv": []any{"t", "dial", "WEB_ADDR", "8080"}, "path": []any{prog + "/bin"}, "format": "go",
		"env": map[string]any{"SALT": salt},
		"services": []any{map[string]any{"name": "web", "argv": []any{"t", "listen", "8080"}, "path": []any{prog + "/bin"},
			"ready": map[string]any{"port": 8080}, "timeoutMs": 10000}},
	}, prog)
	units, err := Units([]*drv.Derivation{d})
	if err != nil || len(units) != 1 {
		t.Fatalf("Units: %v %v", units, err)
	}
	if c, err := Cached(ctx, s, units); err != nil || c["//it:it"] {
		t.Fatalf("cached before the first run: %v %v", c, err)
	}
	start := time.Now()
	if log, err := realise(t, s, d); err != nil {
		t.Fatalf("realise: %v\n%s", err, log)
	}
	t.Logf("test unit with one service: %s", time.Since(start).Round(time.Millisecond))
	if c, err := Cached(ctx, s, units); err != nil || !c["//it:it"] {
		t.Fatalf("not cached after a passing run: %v %v", c, err)
	}
	host := filepath.Join(s.Home(), units[0].Out)
	r, err := ReadResult(host)
	if err != nil || !r.Passed || len(r.Cases) != 1 || r.Cases[0].Name != "TestDial" {
		t.Fatalf("result %+v %v", r, err)
	}
	if tl, _ := os.ReadFile(filepath.Join(host, "test.log")); !strings.Contains(string(tl), "TestDial (0.00s) 127.0.0.2") {
		t.Errorf("test.log %q: the service was not on 127.0.0.2", tl)
	}

	log, err := realise(t, s, d, "--check")
	t.Logf("nix-store --realise --check of a test unit: err=%v\n%s", err, log)
	if err == nil {
		t.Errorf("--check passed; O-02-9 assumed it fails on test timings (re-evaluate Rerun)")
	}

	rr, err := Rerun(units[0], "nonce-1", modResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := Cached(ctx, s, []Unit{rr}); c["//it:it"] {
		t.Fatalf("rerun derivation is already cached")
	}
	if log, err := realise(t, s, rr.Drv); err != nil {
		t.Fatalf("rerun: %v\n%s", err, log)
	}
	if r2, err := ReadResult(filepath.Join(s.Home(), rr.Out)); err != nil || !r2.Passed {
		t.Errorf("rerun result %+v %v", r2, err)
	}
}

// hostPath returns the first path of the host store matching glob that holds bin/<bin>.
func hostPath(t *testing.T, glob, bin string) string {
	t.Helper()
	ms, _ := filepath.Glob(glob)
	for _, m := range ms {
		if fi, err := os.Stat(filepath.Join(m, "bin", bin)); err == nil && !fi.IsDir() {
			return m
		}
	}
	t.Skipf("no %s with bin/%s in the host store", glob, bin)
	return ""
}

// R-FW-025 (a) at the engine level: the postgres service of pkg/services/postgres (closure
// copied from the host store) serves two test units built in parallel by one Nix call, each
// with its own 127.0.0.2:5432; both pass within 3 s and no postgres process remains.
func TestPostgresServiceInNix(t *testing.T) {
	if testing.Short() {
		t.Skip("copies a PostgreSQL closure and builds in the real store")
	}
	hostNix, err := exec.LookPath("nix-store")
	if err != nil {
		t.Skip("no host nix-store to copy PostgreSQL from")
	}
	pg := hostPath(t, "/nix/store/*-postgresql-17.*[0-9]", "postgres")
	bash := hostPath(t, "/nix/store/*-bash-5.*[0-9]", "bash")
	coreutils := hostPath(t, "/nix/store/*-coreutils-9.*[0-9]", "sleep")
	ctx := context.Background()
	s, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	closure, err := exec.Command(hostNix, "-qR", pg, bash, coreutils).Output()
	if err != nil {
		t.Skipf("host closure: %v", err)
	}
	export := exec.Command(hostNix, append([]string{"--export"}, strings.Fields(string(closure))...)...)
	stream, _ := export.StdoutPipe()
	if err := export.Start(); err != nil {
		t.Fatal(err)
	}
	imp := s.Command(ctx, "nix-store", "--import")
	imp.Stdin = stream
	var ilog bytes.Buffer
	imp.Stderr = &ilog
	if err := imp.Run(); err != nil {
		t.Fatalf("import: %v\n%s", err, ilog.String())
	}
	if err := export.Wait(); err != nil {
		t.Fatal(err)
	}

	builder, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prog := importProgram(t, s)
	sp := bob.SpecOf(postgres.Service(nil, postgres.Options{Version: "17", Databases: []string{"app"}}))
	path := []any{pg + "/bin", bash + "/bin", coreutils + "/bin"}
	svc := map[string]any{"name": sp.Name, "argv": sp.Start, "path": path, "env": sp.Env,
		"ready": map[string]any{"argv": sp.Ready.Argv()}, "timeoutMs": sp.Timeout.Milliseconds()}
	salt := fmt.Sprint(time.Now().UnixNano())
	var drvs []*drv.Derivation
	for _, label := range []string{"//it:it", "//it:it2"} {
		d := testJob(t, builder, label, map[string]any{
			"argv": []any{"t", "dial", "POSTGRES_ADDR", "5432"}, "path": []any{prog + "/bin"}, "format": "go",
			"env": map[string]any{"SALT": salt}, "services": []any{svc},
		}, prog, pg, bash, coreutils)
		drvs = append(drvs, d)
	}
	if _, err := s.ImportDrvs(ctx, drvs); err != nil {
		t.Fatal(err)
	}
	p1, _ := drvs[0].Path()
	p2, _ := drvs[1].Path()
	var log bytes.Buffer
	c := s.Command(ctx, "nix-store", "--realise", p1, p2, "--option", "substituters", "", "--option", "max-jobs", "2")
	c.Stderr = &log
	start := time.Now()
	if err := c.Run(); err != nil {
		t.Fatalf("realise: %v\n%s", err, log.String())
	}
	took := time.Since(start)
	t.Logf("two Postgres test units in parallel: %s [shared host]", took.Round(time.Millisecond))
	units, _ := Units(drvs)
	for _, u := range units {
		r, err := ReadResult(filepath.Join(s.Home(), u.Out))
		if err != nil || !r.Passed {
			t.Errorf("%s: %+v %v", u.Label, r, err)
			continue
		}
		t.Logf("%s: %d ms", u.Label, r.DurationMs())
		if r.DurationMs() > 3000 {
			t.Errorf("%s took %d ms, want ≤ 3000", u.Label, r.DurationMs())
		}
	}
	if out, _ := exec.Command("pgrep", "-x", "postgres").Output(); len(out) > 0 {
		t.Errorf("postgres processes remain: %s", out)
	}
}
