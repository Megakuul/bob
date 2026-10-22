package builder

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// env is one sandbox: root/{build,outside/sentinel} and outputs root/out-<name>.
type env struct {
	root, build string
	outs        map[string]string
}

func newEnv(t *testing.T, outputs ...string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{root: root, build: filepath.Join(root, "build"), outs: map[string]string{}}
	for _, d := range []string{filepath.Join(e.build, "tmp"), filepath.Join(root, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "outside", "sentinel"), []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, o := range outputs {
		e.outs[o] = filepath.Join(root, "out-"+o)
	}
	return e
}

func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func st(t testing.TB, op string, args any) bspec.Step {
	return bspec.Step{Op: op, Args: mustJSON(t, args)}
}

func runJobOf(t testing.TB, steps ...bspec.Step) bspec.Job {
	return bspec.Job{V: 1, Kind: "run", Label: "//t:t", Outputs: []string{"out"}, Args: mustJSON(t, bspec.RunArgs{Steps: steps})}
}

// run runs Main in-process with the environment Nix sets and returns the exit code and log.
func (e *env) run(t *testing.T, kind string, job bspec.Job) (int, string) {
	t.Helper()
	file := filepath.Join(e.root, "attrs.json")
	if err := os.WriteFile(file, mustJSON(t, map[string]any{"bobSpec": job, "outputs": e.outs}), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIX_ATTRS_JSON_FILE", file)
	t.Setenv("NIX_BUILD_TOP", e.build)
	t.Setenv("NIX_STORE", "/nix/store")
	var log bytes.Buffer
	code := Main([]string{kind}, Core(), &log, &log)
	return code, log.String()
}

func (e *env) contained(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(e.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range ents {
		if n := de.Name(); n != "build" && n != "attrs.json" && n != "outside" && !strings.HasPrefix(n, "out-") {
			t.Fatalf("the job created %s next to the build directory", n)
		}
	}
	ents, _ = os.ReadDir(filepath.Join(e.root, "outside"))
	b, err := os.ReadFile(filepath.Join(e.root, "outside", "sentinel"))
	if len(ents) != 1 || err != nil || string(b) != "untouched" {
		t.Fatalf("the outside directory changed: %v %q %v", ents, b, err)
	}
}

// Random jobs from hostile path tokens (including ones that are not valid JSON escapes when
// pasted into raw JSON) never write outside and never exit outside 0..4.
func TestFuzzStaysContained(t *testing.T) {
	codes := map[int]int{}
	for _, seed := range []int64{1, 16, 99} {
		rng := rand.New(rand.NewSource(seed))
		e := newEnv(t, "out", "dev")
		escape := filepath.Join(os.TempDir(), fmt.Sprintf("bob-builder-escape-%d", os.Getpid()))
		t.Cleanup(func() { _ = os.RemoveAll(escape) })
		tokens := []string{"..", ".", "/", "//", "", "a", "b/c", e.outs["out"], e.outs["dev"], e.build, e.root,
			filepath.Join(e.root, "outside"), escape, "/etc", "/proc/self", "/dev/null", "/tmp", "link", "\x00", "~",
			"$out", "../../..", "out-out", "a\\..\\b", "l", "l/x"}
		// Most paths start in a writable root so that jobs get past the lexical checks.
		roots := []string{e.outs["out"], e.outs["dev"], e.build}
		path := func() string {
			p := []string{roots[rng.Intn(len(roots))]}
			if rng.Intn(4) == 0 {
				p[0] = tokens[rng.Intn(len(tokens))]
			}
			for range rng.Intn(4) {
				p = append(p, tokens[rng.Intn(len(tokens))])
			}
			return strings.Join(p, "/")
		}
		ops := []string{"mkdir", "write", "copy", "symlink", "exec", "check-outputs", "mkdir", "write", "symlink", "bogus"}
		for i := range 150 {
			var steps []bspec.Step
			for range 1 + rng.Intn(3) {
				switch op := ops[rng.Intn(len(ops))]; op {
				case "mkdir":
					steps = append(steps, st(t, op, bspec.MkdirArgs{Path: path()}))
				case "write":
					steps = append(steps, st(t, op, bspec.WriteArgs{Path: path(), Content: "fuzz", Mode: uint32(rng.Intn(0o1000))}))
				case "copy":
					steps = append(steps, st(t, op, bspec.CopyArgs{From: path(), To: path()}))
				case "symlink":
					steps = append(steps, st(t, op, bspec.SymlinkArgs{Target: path(), Link: path()}))
				case "exec":
					steps = append(steps, st(t, op, bspec.ExecArgs{Argv: []string{"true"}, Cwd: path(), Path: []string{"/usr/bin", "/bin"}}))
				default:
					steps = append(steps, st(t, op, map[string]string{"path": path()}))
				}
			}
			code, log := e.run(t, "run", runJobOf(t, steps...))
			codes[code]++
			if code < 0 || code > 4 {
				t.Fatalf("seed %d job %d: exit %d:\n%s", seed, i, code, log)
			}
			e.contained(t)
			if _, err := os.Lstat(escape); err == nil {
				t.Fatalf("seed %d job %d wrote %s", seed, i, escape)
			}
		}
	}
	// The generator must reach the ops, not only the lexical checks.
	if codes[ExitOK] == 0 || codes[ExitStep] == 0 || codes[ExitInvalid] == 0 {
		t.Errorf("exit code spread %v: the fuzzer does not exercise success, failure and refusal", codes)
	}
	t.Logf("exit codes: %v", codes)
}

// Writing through a symlink the job created is refused; the target outside stays untouched.
func TestNoWritesThroughSymlinks(t *testing.T) {
	e := newEnv(t, "out")
	out := e.outs["out"]
	outside := filepath.Join(e.root, "outside")
	for _, c := range []struct {
		name string
		step bspec.Step
		want int
	}{
		{"write-below", st(t, "write", bspec.WriteArgs{Path: filepath.Join(out, "l", "x"), Content: "x"}), ExitInvalid},
		{"mkdir-below", st(t, "mkdir", bspec.MkdirArgs{Path: filepath.Join(out, "l", "d")}), ExitInvalid},
		{"write-final", st(t, "write", bspec.WriteArgs{Path: filepath.Join(out, "s"), Content: "x"}), ExitStep},
		{"copy-final", st(t, "copy", bspec.CopyArgs{From: "/etc/hostname", To: filepath.Join(out, "s")}), ExitStep},
	} {
		t.Run(c.name, func(t *testing.T) {
			job := runJobOf(t,
				st(t, "mkdir", bspec.MkdirArgs{Path: out}),
				st(t, "symlink", bspec.SymlinkArgs{Target: outside, Link: filepath.Join(out, "l")}),
				st(t, "symlink", bspec.SymlinkArgs{Target: filepath.Join(outside, "sentinel"), Link: filepath.Join(out, "s")}),
				c.step)
			_ = os.RemoveAll(out)
			if code, log := e.run(t, "run", job); code != c.want {
				t.Errorf("exit %d, want %d:\n%s", code, c.want, log)
			}
			e.contained(t)
		})
	}
	t.Run("output-is-symlink", func(t *testing.T) {
		_ = os.RemoveAll(out)
		job := runJobOf(t,
			st(t, "symlink", bspec.SymlinkArgs{Target: outside, Link: out}),
			st(t, "write", bspec.WriteArgs{Path: filepath.Join(out, "x"), Content: "x"}))
		if code, log := e.run(t, "run", job); code != ExitInvalid {
			t.Errorf("exit %d, want 2:\n%s", code, log)
		}
		e.contained(t)
	})
}

func TestCopyIntoItselfIsInvalid(t *testing.T) {
	e := newEnv(t, "out")
	out := e.outs["out"]
	job := runJobOf(t,
		st(t, "mkdir", bspec.MkdirArgs{Path: filepath.Join(out, "a")}),
		st(t, "copy", bspec.CopyArgs{From: out, To: filepath.Join(out, "a", "b")}))
	if code, log := e.run(t, "run", job); code != ExitInvalid {
		t.Errorf("exit %d, want 2:\n%s", code, log)
	}
}

type tarEntry struct {
	name, link string
	typ        byte
	body       string
}

func tarFile(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, en := range entries {
		h := &tar.Header{Name: en.name, Linkname: en.link, Typeflag: en.typ, Mode: 0o644, Size: int64(len(en.body))}
		if en.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, en.body); err != nil && en.typ == tar.TypeReg {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "a.tar")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Unsafe archive entries fail the step (exit 1) and write nothing outside.
func TestUnpackRefusesUnsafeEntries(t *testing.T) {
	for _, c := range []struct {
		name    string
		entries []tarEntry
	}{
		{"dotdot", []tarEntry{{name: "p/../../x", typ: tar.TypeReg, body: "x"}}},
		{"absolute", []tarEntry{{name: "/x", typ: tar.TypeReg, body: "x"}}},
		{"symlink-escape", []tarEntry{{name: "p/l", link: "../../outside", typ: tar.TypeSymlink}}},
		{"through-symlink", []tarEntry{{name: "p/l", link: "d", typ: tar.TypeSymlink}, {name: "p/l/x", typ: tar.TypeReg, body: "x"}}},
		{"device", []tarEntry{{name: "p/dev", typ: tar.TypeChar}}},
		{"fifo", []tarEntry{{name: "p/f", typ: tar.TypeFifo}}},
		{"hardlink-missing", []tarEntry{{name: "p/h", link: "p/none", typ: tar.TypeLink}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "out")
			job := bspec.Job{V: 1, Kind: "unpack", Label: "//t:u", Outputs: []string{"out"},
				Args: mustJSON(t, bspec.UnpackArgs{Archive: tarFile(t, c.entries...), To: e.outs["out"], Strip: 1})}
			if code, log := e.run(t, "unpack", job); code != ExitStep {
				t.Errorf("exit %d, want 1:\n%s", code, log)
			}
			e.contained(t)
		})
	}
	t.Run("hardlink-copies", func(t *testing.T) {
		e := newEnv(t, "out")
		arch := tarFile(t, tarEntry{name: "p/a", typ: tar.TypeReg, body: "data"}, tarEntry{name: "p/h", link: "p/a", typ: tar.TypeLink})
		job := bspec.Job{V: 1, Kind: "unpack", Label: "//t:u", Outputs: []string{"out"},
			Args: mustJSON(t, bspec.UnpackArgs{Archive: arch, To: e.outs["out"], Strip: 1})}
		if code, log := e.run(t, "unpack", job); code != 0 {
			t.Fatalf("exit %d:\n%s", code, log)
		}
		if b, err := os.ReadFile(filepath.Join(e.outs["out"], "h")); err != nil || string(b) != "data" {
			t.Errorf("hard link copy: %q %v", b, err)
		}
	})
}

// A hash split across two reads is still found; 31 matching characters are not.
func TestScannerAcrossChunks(t *testing.T) {
	const h = "0123456789abcdfghijklmnpqrsvwxyz"
	s := &scanner{want: map[string]string{h: "/nix/store/" + h + "-x"}}
	s.scan([]byte("prefix " + h[:10]))
	s.scan([]byte(h[10:] + " suffix"))
	if s.found == "" {
		t.Errorf("hash across chunks not found")
	}
	s = &scanner{want: map[string]string{h: "p"}}
	s.scan([]byte(h[:31] + "_" + h[1:]))
	if s.found != "" {
		t.Errorf("found a hash that is not there")
	}
}
