package nix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// jobEnv is the sandbox of one bob-builder run: a scratch root with the build directory
// (NIX_BUILD_TOP), one directory per output (created by the job, not before), and a sentinel
// directory outside everything the job may write to.
type jobEnv struct {
	root, build string
	outs        map[string]string
}

func newJobEnv(t testing.TB, outputs ...string) *jobEnv {
	t.Helper()
	root := t.TempDir()
	j := &jobEnv{root: root, build: filepath.Join(root, "build"), outs: map[string]string{}}
	for _, d := range []string{j.build, filepath.Join(j.build, "tmp"), filepath.Join(root, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(root, "outside", "sentinel"), []byte("untouched"), 0o644)
	for _, o := range outputs {
		j.outs[o] = filepath.Join(root, "out-"+o)
	}
	return j
}

func raw(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func step(t testing.TB, op string, args any) bspec.Step {
	t.Helper()
	return bspec.Step{Op: op, Args: raw(t, args)}
}

func runJob(t testing.TB, steps ...bspec.Step) bspec.Job {
	return bspec.Job{V: 1, Kind: "run", Label: "//t:t", Outputs: []string{"out"}, Args: raw(t, bspec.RunArgs{Steps: steps})}
}

// run writes the structured-attributes file Nix would give the builder and runs
// `bob-builder <kind>` with the environment Nix sets.
func (j *jobEnv) run(t testing.TB, kind string, job bspec.Job, env ...string) harness.Result {
	t.Helper()
	attrs := map[string]any{"name": "t", "system": "aarch64-linux", "bobSpec": job, "outputs": j.outs}
	checks := map[string]any{}
	for o := range j.outs {
		checks[o] = map[string]any{"disallowedReferences": job.Fixup.Forbidden}
	}
	attrs["outputChecks"] = checks
	file := filepath.Join(j.root, "attrs.json")
	mustWrite(t, file, raw(t, attrs), 0o644)
	e := []string{"NIX_ATTRS_JSON_FILE=" + file, "NIX_BUILD_TOP=" + j.build, "TMPDIR=" + filepath.Join(j.build, "tmp"),
		"NIX_BUILD_CORES=1", "NIX_STORE=/nix/store", "SECRET_HOST_VAR=leaked"}
	for o, p := range j.outs {
		e = append(e, o+"="+p)
	}
	return harness.RunPath(t, harness.Bin(t, "bob-builder"), harness.Spec{Dir: j.build, Env: append(e, env...)}, kind)
}

// outside fails the test if anything under root other than build and the outputs changed.
func (j *jobEnv) assertContained(t testing.TB) {
	t.Helper()
	ents, err := os.ReadDir(j.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range ents {
		n := de.Name()
		if n == "build" || n == "attrs.json" || n == "outside" || strings.HasPrefix(n, "out-") {
			continue
		}
		t.Errorf("the job created %s next to its build directory and outputs", n)
	}
	if b, err := os.ReadFile(filepath.Join(j.root, "outside", "sentinel")); err != nil || string(b) != "untouched" {
		t.Errorf("the sentinel outside the sandbox changed: %q %v", b, err)
	}
	ents, _ = os.ReadDir(filepath.Join(j.root, "outside"))
	if len(ents) != 1 {
		t.Errorf("files appeared outside the sandbox: %v", ents)
	}
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[strings.LastIndex(s, "\n")+1:]
}

// goBuilderBinary builds cmd/bob-builder the way R-NIX-015 prescribes.
func goBuilderBinary(t testing.TB, srcRoot, goarch, out string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", out, "./cmd/bob-builder")
	cmd.Dir = srcRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: go build bob-builder (%s): %v: %s", goarch, err, b)
	}
}

// TestRNIX015BuilderReproducible: building cmd/bob-builder twice (two checkouts) gives identical
// bytes; BuilderVersion starts at 1.
func TestRNIX015BuilderReproducible(t *testing.T) {
	parity.Covers(t, "R-NIX-015")
	if bspec.BuilderVersion < 1 {
		t.Errorf("BuilderVersion = %d, want an integer starting at 1", bspec.BuilderVersion)
	}
	root := harness.Root(t)
	second := filepath.Join(t.TempDir(), "checkout")
	for _, d := range []string{"cmd/bob-builder", "pkg"} {
		if err := os.CopyFS(filepath.Join(second, filepath.FromSlash(d)), os.DirFS(filepath.Join(root, filepath.FromSlash(d)))); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"go.mod", "go.sum"} {
		mustWrite(t, filepath.Join(second, f), mustRead(t, filepath.Join(root, f)), 0o644)
	}
	out := t.TempDir()
	goBuilderBinary(t, root, runtime.GOARCH, filepath.Join(out, "a"))
	goBuilderBinary(t, second, runtime.GOARCH, filepath.Join(out, "b"))
	if !bytes.Equal(mustRead(t, filepath.Join(out, "a")), mustRead(t, filepath.Join(out, "b"))) {
		t.Errorf("two builds of cmd/bob-builder from two checkouts differ")
	}
}

// TestRNIX015EmbeddedTableMatchesBuilder: the table in pkg/engine/store/builders.go has the
// source path of tree bin/bob-builder, named bob-builder-<ver>, for both systems; a changed
// builder without regenerating the table makes this test fail (the CI check of R-NIX-015).
func TestRNIX015EmbeddedTableMatchesBuilder(t *testing.T) {
	parity.Covers(t, "R-NIX-015")
	root := harness.Root(t)
	for _, c := range []struct{ system, goarch string }{{"aarch64-linux", "arm64"}, {"x86_64-linux", "amd64"}} {
		t.Run(c.system, func(t *testing.T) {
			recorded, err := store.BuilderPath(c.system)
			if notImplemented(t, err, "store.BuilderPath") {
				return
			}
			if err != nil {
				t.Fatalf("store.BuilderPath: %v", err)
			}
			tree := t.TempDir()
			goBuilderBinary(t, root, c.goarch, filepath.Join(tree, "bin", "bob-builder"))
			if err := os.Chmod(filepath.Join(tree, "bin", "bob-builder"), 0o755); err != nil {
				t.Fatal(err)
			}
			want, err := srcs.Path(fmt.Sprintf("bob-builder-%d", bspec.BuilderVersion), srcs.Tree{Root: tree, Files: []string{"bin/bob-builder"}})
			if err != nil {
				t.Fatalf("srcs.Path: %v", err)
			}
			if recorded != want {
				t.Errorf("builders.go records %s, the builder source builds to %s (go generate ./pkg/engine/store)", recorded, want)
			}
		})
	}
}

// TestRNIX016RunOps is acceptance (b) for kind `run`: every op, the exact exec environment, and
// check-outputs.
func TestRNIX016RunOps(t *testing.T) {
	parity.Covers(t, "R-NIX-016")
	j := newJobEnv(t, "out")
	out := j.outs["out"]
	data := filepath.Join(j.root, "build", "data")
	job := runJob(t,
		step(t, "mkdir", bspec.MkdirArgs{Path: filepath.Join(out, "a", "b")}),
		step(t, "write", bspec.WriteArgs{Path: filepath.Join(out, "a", "b", "f.txt"), Content: "hello <&> \"é\"\n"}),
		step(t, "write", bspec.WriteArgs{Path: filepath.Join(out, "bin", "tool"), Content: "#!/x\n", Mode: 0o755}),
		step(t, "mkdir", bspec.MkdirArgs{Path: data}),
		step(t, "write", bspec.WriteArgs{Path: filepath.Join(data, "src.txt"), Content: "copy me"}),
		step(t, "copy", bspec.CopyArgs{From: filepath.Join(data, "src.txt"), To: filepath.Join(out, "copied.txt")}),
		step(t, "copy", bspec.CopyArgs{From: filepath.Join(out, "a"), To: filepath.Join(out, "a2")}),
		step(t, "symlink", bspec.SymlinkArgs{Target: "a/b/f.txt", Link: filepath.Join(out, "link")}),
		step(t, "exec", bspec.ExecArgs{Argv: []string{"env"}, Env: map[string]string{"STEP_VAR": "yes"}, Path: []string{"/usr/bin"}}),
		step(t, "check-outputs", struct{}{}),
	)
	r := j.run(t, "run", job)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("exit %d:\n%s", r.Exit, r.Combined())
	}
	read := func(p string) string { return string(mustRead(t, filepath.Join(out, filepath.FromSlash(p)))) }
	if got := read("a/b/f.txt"); got != "hello <&> \"é\"\n" {
		t.Errorf("write: %q", got)
	}
	if fi, err := os.Stat(filepath.Join(out, "bin", "tool")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("write with mode 0755: %v %v", fi, err)
	}
	if read("copied.txt") != "copy me" || read("a2/b/f.txt") != read("a/b/f.txt") {
		t.Errorf("copy of a file or a tree did not copy the content")
	}
	if l, err := os.Readlink(filepath.Join(out, "link")); err != nil || l != "a/b/f.txt" {
		t.Errorf("symlink: %q %v", l, err)
	}
	// exec: exactly PATH (declared), HOME, TMPDIR, NIX_BUILD_CORES, SOURCE_DATE_EPOCH, TZ, LC_ALL,
	// the outputs and the step's env.
	got := map[string]string{}
	for _, l := range strings.Split(r.Stdout+r.Stderr, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.Contains(k, " ") && k != "" {
			got[k] = v
		}
	}
	want := map[string]string{
		"PATH": "/usr/bin", "HOME": "/homeless-shelter", "TMPDIR": filepath.Join(j.build, "tmp"), "NIX_BUILD_CORES": "1",
		"SOURCE_DATE_EPOCH": "1", "TZ": "UTC", "LC_ALL": "C", "out": out, "STEP_VAR": "yes",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("exec env %s = %q, want %q", k, got[k], v)
		}
	}
	if _, leaked := got["SECRET_HOST_VAR"]; leaked {
		t.Errorf("the builder's own environment leaked into exec")
	}
	for k := range got {
		if _, ok := want[k]; !ok && !strings.HasPrefix(k, "@nix") && !strings.HasPrefix(k, "bob") {
			t.Errorf("exec saw an unexpected variable %s", k)
		}
	}
}

// TestRNIX016UnpackAndCopyTree covers kinds `unpack` and `copy-tree`.
func TestRNIX016UnpackAndCopyTree(t *testing.T) {
	parity.Covers(t, "R-NIX-016")
	o := newOracle(t)
	expected := t.TempDir()
	normalisedTree(t, expected, true)
	wantNar := o.narHashHex(expected)

	j := newJobEnv(t, "out")
	archive, err := filepath.Abs(filepath.Join("testdata", "archives", "tree.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(j.outs["out"], "tree")
	ua := bspec.UnpackArgs{Archive: archive, To: to, Strip: 1}
	job := bspec.Job{V: 1, Kind: "unpack", Label: "//t:unpack", Outputs: []string{"out"}, Args: raw(t, ua)}
	r := j.run(t, "unpack", job)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("unpack exit %d:\n%s", r.Exit, r.Combined())
	}
	if got := o.narHashHex(to); got != wantNar {
		t.Errorf("unpacked tree NAR hash %s, want %s", got, wantNar)
	}
	j2 := newJobEnv(t, "out")
	dst := filepath.Join(j2.outs["out"], "copy")
	ct := bspec.Job{V: 1, Kind: "copy-tree", Label: "//t:ct", Outputs: []string{"out"}, Args: raw(t, bspec.CopyTreeArgs{From: expected, To: dst})}
	r = j2.run(t, "copy-tree", ct)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("copy-tree exit %d:\n%s", r.Exit, r.Combined())
	}
	if got := o.narHashHex(dst); got != wantNar {
		t.Errorf("copied tree NAR hash %s, want %s (modes and symlinks must be kept)", got, wantNar)
	}
}

// TestRNIX016InvalidJobs: an unknown kind, a kind that differs from argv[1], a wrong version, a
// missing attrs file and writes outside the outputs and /build all exit 2 and change nothing.
func TestRNIX016InvalidJobs(t *testing.T) {
	parity.Covers(t, "R-NIX-016", "R-NIX-017")
	j := newJobEnv(t, "out")
	outside := filepath.Join(j.root, "outside", "evil")
	good := runJob(t, step(t, "mkdir", bspec.MkdirArgs{Path: j.outs["out"]}))
	cases := []struct {
		name string
		kind string
		job  bspec.Job
	}{
		{"unknown-kind", "no-such-kind", bspec.Job{V: 1, Kind: "no-such-kind", Label: "//t:t", Outputs: []string{"out"}, Args: raw(t, struct{}{})}},
		{"kind-mismatch", "unpack", good},
		{"version", "run", func() bspec.Job { g := good; g.V = 2; return g }()},
		{"write-outside", "run", runJob(t, step(t, "write", bspec.WriteArgs{Path: outside, Content: "x"}))},
		{"mkdir-outside", "run", runJob(t, step(t, "mkdir", bspec.MkdirArgs{Path: outside}))},
		{"dotdot-out-of-output", "run", runJob(t, step(t, "write", bspec.WriteArgs{Path: j.outs["out"] + "/../outside/evil", Content: "x"}))},
		{"symlink-outside", "run", runJob(t, step(t, "symlink", bspec.SymlinkArgs{Target: "x", Link: outside}))},
		{"copy-to-outside", "run", runJob(t, step(t, "copy", bspec.CopyArgs{From: "/etc/hostname", To: outside}))},
		{"exec-cwd-outside", "run", runJob(t, step(t, "exec", bspec.ExecArgs{Argv: []string{"touch", "evil"}, Cwd: filepath.Join(j.root, "outside"), Path: []string{"/usr/bin"}}))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := j.run(t, c.kind, c.job)
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 2 {
				t.Errorf("exit %d, want 2 (invalid job):\n%s", r.Exit, r.Combined())
			}
			j.assertContained(t)
		})
	}
	t.Run("no-attrs-file", func(t *testing.T) {
		r := harness.RunPath(t, harness.Bin(t, "bob-builder"), harness.Spec{Dir: j.build, Env: []string{"NIX_BUILD_TOP=" + j.build}}, "run")
		if harness.Red(t, r) {
			return
		}
		if r.Exit != 2 {
			t.Errorf("exit %d without NIX_ATTRS_JSON_FILE, want 2", r.Exit)
		}
	})
}

// TestRNIX016FuzzedJobsStayContained is acceptance (c): random jobs built from hostile path
// tokens never write outside the outputs and the build directory.
func TestRNIX016FuzzedJobsStayContained(t *testing.T) {
	parity.Covers(t, "R-NIX-016")
	rng := rand.New(rand.NewSource(16))
	j := newJobEnv(t, "out")
	escape := filepath.Join(os.TempDir(), fmt.Sprintf("bob-fuzz-escape-%d", os.Getpid()))
	t.Cleanup(func() { _ = os.RemoveAll(escape) })
	tokens := []string{"..", ".", "/", "//", "", "a", "b/c", j.outs["out"], j.build, j.root, filepath.Join(j.root, "outside"), escape,
		"/etc", "/proc/self", "/dev/null", "/tmp", "link", "\x00", "~", "$out", "${out}", "../../..", "out-out", "a\\..\\b"}
	path := func() string {
		var p []string
		for i := 0; i < 1+rng.Intn(4); i++ {
			p = append(p, tokens[rng.Intn(len(tokens))])
		}
		return strings.Join(p, "/")
	}
	ops := []string{"mkdir", "write", "copy", "symlink", "exec", "check-outputs", "bogus"}
	for i := 0; i < 60; i++ {
		var steps []bspec.Step
		for k := 0; k < 1+rng.Intn(5); k++ {
			switch op := ops[rng.Intn(len(ops))]; op {
			case "mkdir":
				steps = append(steps, step(t, op, bspec.MkdirArgs{Path: path()}))
			case "write":
				steps = append(steps, step(t, op, bspec.WriteArgs{Path: path(), Content: "fuzz", Mode: uint32(rng.Intn(0o1000))}))
			case "copy":
				steps = append(steps, step(t, op, bspec.CopyArgs{From: path(), To: path()}))
			case "symlink":
				steps = append(steps, step(t, op, bspec.SymlinkArgs{Target: path(), Link: path()}))
			case "exec":
				steps = append(steps, step(t, op, bspec.ExecArgs{Argv: []string{"true"}, Cwd: path(), Path: []string{"/usr/bin"}}))
			default:
				steps = append(steps, step(t, op, map[string]string{"path": path()}))
			}
		}
		r := j.run(t, "run", runJob(t, steps...))
		if harness.Red(t, r) {
			return
		}
		if r.Exit < 0 || r.Exit > 4 {
			t.Fatalf("job %d: exit %d (crash?):\n%s", i, r.Exit, r.Combined())
		}
		j.assertContained(t)
		if _, err := os.Lstat(escape); err == nil {
			t.Fatalf("job %d wrote %s outside the sandbox", i, escape)
		}
	}
}

// TestRNIX016MiBJobBuilds is acceptance (d): a 1 MiB job builds (no environment-size limit).
func TestRNIX016MiBJobBuilds(t *testing.T) {
	parity.Covers(t, "R-NIX-016")
	j := newJobEnv(t, "out")
	payload := strings.Repeat("0123456789abcdef", 64*1024) // 1 MiB of content inside the job
	job := runJob(t,
		step(t, "mkdir", bspec.MkdirArgs{Path: j.outs["out"]}),
		step(t, "write", bspec.WriteArgs{Path: filepath.Join(j.outs["out"], "big"), Content: payload}),
	)
	if n := len(raw(t, job)); n < 1<<20 {
		t.Fatalf("harness: the job is only %d bytes", n)
	}
	r := j.run(t, "run", job)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("exit %d:\n%.2000s", r.Exit, r.Combined())
	}
	if got := mustRead(t, filepath.Join(j.outs["out"], "big")); string(got) != payload {
		t.Errorf("output differs from the 1 MiB payload (%d bytes)", len(got))
	}
}

// TestRNIX017LogsAndExitCodes: setPhase lines per op, exit 1 for a failing step with the step and
// argv[0] on the last line, 4 for a missing declared output, 0 for a step that prints
// `@nix {"action":"msg"…}`.
func TestRNIX017LogsAndExitCodes(t *testing.T) {
	parity.Covers(t, "R-NIX-017")
	j := newJobEnv(t, "out")
	mk := step(t, "mkdir", bspec.MkdirArgs{Path: j.outs["out"]})
	r := j.run(t, "run", runJob(t, mk,
		step(t, "write", bspec.WriteArgs{Path: filepath.Join(j.outs["out"], "f"), Content: "x"}),
		step(t, "exec", bspec.ExecArgs{Argv: []string{"printf", "plain line\\n@nix {\"action\":\"msg\",\"level\":0,\"msg\":\"fake error: build failed\"}\\n"}, Path: []string{"/usr/bin"}}),
	))
	if harness.Red(t, r) {
		return
	}
	out := r.Stdout + r.Stderr
	if r.Exit != 0 {
		t.Errorf("a step printing an @nix msg line changed the result: exit %d\n%s", r.Exit, out)
	}
	for _, op := range []string{"mkdir", "write", "exec"} {
		if !strings.Contains(out, `@nix {"action":"setPhase","phase":"`+op+`"}`) {
			t.Errorf("no setPhase line for op %s in:\n%s", op, out)
		}
	}
	if !strings.Contains(out, "plain line") {
		t.Errorf("the step's output is not in the log:\n%s", out)
	}

	j = newJobEnv(t, "out")
	r = j.run(t, "run", runJob(t, step(t, "mkdir", bspec.MkdirArgs{Path: j.outs["out"]}),
		step(t, "exec", bspec.ExecArgs{Argv: []string{"false"}, Path: []string{"/usr/bin"}})))
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 1 {
		t.Errorf("failing step: exit %d, want 1", r.Exit)
	}
	if l := lastLine(r.Stdout + r.Stderr); !strings.Contains(l, "exec") || !strings.Contains(l, "false") {
		t.Errorf("the last line must name the step and argv[0], got %q", l)
	}

	j = newJobEnv(t, "out", "dev")
	job := runJob(t, step(t, "mkdir", bspec.MkdirArgs{Path: j.outs["out"]}))
	job.Outputs = []string{"out", "dev"}
	r = j.run(t, "run", job)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 4 {
		t.Errorf("declared output dev missing: exit %d, want 4\n%s", r.Exit, r.Combined())
	}
}

// ---------------------------------------------------------------- R-NIX-019 fixup (builder part)

// hostTool finds a binary on PATH or in the host's Nix store.
func hostTool(t testing.TB, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	m, _ := filepath.Glob("/nix/store/*-" + name + "-*/bin/" + name)
	if len(m) == 0 {
		m, _ = filepath.Glob("/nix/store/*binutils-[0-9]*/bin/" + name)
	}
	if len(m) == 0 {
		t.Fatalf("harness: %s is needed to test the fixup and is not on this host", name)
	}
	return m[len(m)-1]
}

// goELF builds a small static Go executable with DWARF into path.
func goELF(t testing.TB, path string) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), []byte("module hello\n\ngo 1.21\n"), 0o644)
	mustWrite(t, filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n"), 0o644)
	cmd := exec.Command("go", "build", "-o", path, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: %v: %s", err, b)
	}
}

func sections(t testing.TB, elf string) string {
	t.Helper()
	b, err := exec.Command(hostTool(t, "readelf"), "-S", "-W", elf).CombinedOutput()
	if err != nil {
		t.Fatalf("harness: readelf %s: %v: %s", elf, err, b)
	}
	return string(b)
}

func fixupTools(t testing.TB) bspec.Tools {
	return bspec.Tools{Strip: hostTool(t, "strip"), Objcopy: hostTool(t, "objcopy"), Patchelf: hostTool(t, "patchelf")}
}

// TestRNIX019FixupStripsAndKeepsDebug: ELF files under bin/ lose their debug info; profile debug
// (Strip false) keeps it; with DebugOut the info moves to output `debug` with a debuglink.
func TestRNIX019FixupStripsAndKeepsDebug(t *testing.T) {
	parity.Covers(t, "R-NIX-019")
	elf := filepath.Join(t.TempDir(), "hello")
	goELF(t, elf)
	if !strings.Contains(sections(t, elf), ".debug_info") {
		t.Fatal("harness: the test executable has no .debug_info")
	}
	prep := func(j *jobEnv) []bspec.Step {
		return []bspec.Step{
			step(t, "mkdir", bspec.MkdirArgs{Path: filepath.Join(j.outs["out"], "bin")}),
			step(t, "copy", bspec.CopyArgs{From: elf, To: filepath.Join(j.outs["out"], "bin", "hello")}),
		}
	}
	t.Run("release", func(t *testing.T) {
		j := newJobEnv(t, "out")
		job := runJob(t, prep(j)...)
		job.Fixup = bspec.Fixup{Strip: true, Tools: fixupTools(t)}
		r := j.run(t, "run", job)
		if harness.Red(t, r) {
			return
		}
		if r.Exit != 0 {
			t.Fatalf("exit %d:\n%s", r.Exit, r.Combined())
		}
		if s := sections(t, filepath.Join(j.outs["out"], "bin", "hello")); strings.Contains(s, ".debug_info") {
			t.Errorf("the executable still has .debug_info after fixup:\n%s", s)
		}
	})
	t.Run("debug-profile", func(t *testing.T) {
		j := newJobEnv(t, "out")
		job := runJob(t, prep(j)...)
		job.Fixup = bspec.Fixup{Strip: false, Tools: fixupTools(t)}
		r := j.run(t, "run", job)
		if harness.Red(t, r) {
			return
		}
		if r.Exit != 0 {
			t.Fatalf("exit %d:\n%s", r.Exit, r.Combined())
		}
		if s := sections(t, filepath.Join(j.outs["out"], "bin", "hello")); !strings.Contains(s, ".debug_info") {
			t.Errorf("profile debug must not strip, but .debug_info is gone")
		}
	})
	t.Run("debug-output", func(t *testing.T) {
		j := newJobEnv(t, "out", "debug")
		job := runJob(t, prep(j)...)
		job.Outputs = []string{"out", "debug"}
		job.Fixup = bspec.Fixup{Strip: false, DebugOut: true, Tools: fixupTools(t)}
		r := j.run(t, "run", job)
		if harness.Red(t, r) {
			return
		}
		if r.Exit != 0 {
			t.Fatalf("exit %d:\n%s", r.Exit, r.Combined())
		}
		bin := filepath.Join(j.outs["out"], "bin", "hello")
		if s := sections(t, bin); strings.Contains(s, ".debug_info") || !strings.Contains(s, ".gnu_debuglink") {
			t.Errorf("out binary must lose .debug_info and gain .gnu_debuglink:\n%s", s)
		}
		var found bool
		_ = filepath.WalkDir(j.outs["debug"], func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.Contains(sections(t, p), ".debug_info") {
				found = true
			}
			return nil
		})
		if !found {
			t.Errorf("no file with .debug_info in output debug")
		}
	})
}

// TestRNIX019FixupShrinksRunpath: RUNPATH entries outside /nix/store (like /build) are dropped.
func TestRNIX019FixupShrinksRunpath(t *testing.T) {
	parity.Covers(t, "R-NIX-019")
	patchelf := hostTool(t, "patchelf")
	sample := filepath.Join(t.TempDir(), "dyn")
	b := mustRead(t, "/usr/bin/true")
	mustWrite(t, sample, b, 0o755)
	if out, err := exec.Command(patchelf, "--force-rpath", "--set-rpath", "/build/lib:/nix/store/00000000000000000000000000000000-zz/lib", sample).CombinedOutput(); err != nil {
		t.Fatalf("harness: patchelf: %v: %s", err, out)
	}
	j := newJobEnv(t, "out")
	job := runJob(t,
		step(t, "mkdir", bspec.MkdirArgs{Path: filepath.Join(j.outs["out"], "bin")}),
		step(t, "copy", bspec.CopyArgs{From: sample, To: filepath.Join(j.outs["out"], "bin", "dyn")}),
	)
	job.Fixup = bspec.Fixup{Strip: true, ShrinkRPath: true, Tools: fixupTools(t)}
	r := j.run(t, "run", job)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("exit %d:\n%s", r.Exit, r.Combined())
	}
	got, err := exec.Command(patchelf, "--print-rpath", filepath.Join(j.outs["out"], "bin", "dyn")).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range strings.Split(strings.TrimSpace(string(got)), ":") {
		if e != "" && !strings.HasPrefix(e, "/nix/store/") {
			t.Errorf("RUNPATH still has the entry %q outside /nix/store (%s)", e, got)
		}
	}
}

// TestRNIX019ReferenceCheck: an output that contains the hash of a forbidden store path fails with
// exit 3 (9003) naming the reference; an output without it passes.
func TestRNIX019ReferenceCheck(t *testing.T) {
	parity.Covers(t, "R-NIX-019")
	const forbidden = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-gcc-15.3.0"
	for _, c := range []struct {
		name, content string
		exit          int
	}{
		{"clean", "no references here", 0},
		{"forbidden", "built with " + forbidden + "/bin/gcc", 3},
		{"hash-only", "x0123456789abcdfghijklmnpqrsvwxyzx", 3},
		{"lookalike", "0123456789abcdfghijklmnpqrsvwxy", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			j := newJobEnv(t, "out")
			job := runJob(t,
				step(t, "mkdir", bspec.MkdirArgs{Path: filepath.Join(j.outs["out"], "bin")}),
				step(t, "write", bspec.WriteArgs{Path: filepath.Join(j.outs["out"], "bin", "prog"), Content: c.content, Mode: 0o755}),
			)
			job.Fixup = bspec.Fixup{Forbidden: []string{forbidden}}
			r := j.run(t, "run", job)
			if harness.Red(t, r) {
				return
			}
			if r.Exit != c.exit {
				t.Errorf("exit %d, want %d:\n%s", r.Exit, c.exit, r.Combined())
			}
			if c.exit == 3 && !strings.Contains(r.Stdout+r.Stderr, "0123456789abcdfghijklmnpqrsvwxyz-gcc-15.3.0") {
				t.Errorf("the violation does not name the forbidden path:\n%s", r.Combined())
			}
		})
	}
}
