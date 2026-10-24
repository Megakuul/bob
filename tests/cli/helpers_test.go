package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
)

// home returns a fresh HOME directory and the environment that makes bob use it for all of its
// state (no credentials.json, no config.json, empty store).
func home(t testing.TB) []string {
	t.Helper()
	h := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(h, 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"HOME=" + h,
		"XDG_CONFIG_HOME=" + filepath.Join(h, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(h, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(h, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(h, ".cache"),
		"TERM=dumb",
	}
}

// homeDir extracts the HOME of an environment made by home.
func homeDir(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "HOME="); ok {
			return v
		}
	}
	return ""
}

// bob runs the bob binary in dir with env and args (a long timeout: builds are real).
func bob(t testing.TB, dir string, env []string, args ...string) harness.Result {
	t.Helper()
	return harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Timeout: 4 * time.Minute}, args...)
}

// noLeak fails t when r's output (shown without -v) contains raw Nix text.
func noLeak(t testing.TB, r harness.Result) {
	t.Helper()
	for _, l := range leakcheck.Check([]byte(r.Combined())) {
		t.Errorf("raw Nix text in output of bob %s: %s", strings.Join(r.Args, " "), l)
	}
}

// repo copies testdata/repos/<name> into a fresh git repository and returns its directory.
func repo(t testing.TB, name string) string {
	t.Helper()
	src := filepath.Join(harness.Root(t), "tests", "cli", "testdata", "repos", name)
	dst := filepath.Join(t.TempDir(), name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("harness: copy repo %s: %v", name, err)
	}
	gitInit(t, dst)
	return dst
}

func gitInit(t testing.TB, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("harness: git %v: %v: %s", args, err, b)
		}
	}
}

// lines splits output into lines without the trailing empty one.
func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// tree hashes the content of every file under dir (excluding .git), for "changes no file".
func tree(t testing.TB, dir string) string {
	t.Helper()
	var b bytes.Buffer
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.Type()&fs.ModeSymlink != 0 {
			// A link (e.g. to a directory in node_modules) is hashed by its target (correction 88).
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			b.WriteString(rel + "\x00link:" + target + "\x00")
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b.WriteString(rel + "\x00")
		b.Write(data)
		b.WriteString("\x00")
		return nil
	})
	if err != nil {
		t.Fatalf("harness: hash tree: %v", err)
	}
	return b.String()
}

// event is one decoded --json line.
type event map[string]any

// events decodes every stdout line of a --json run. Any line that is not a JSON object is an
// error ("nothing else on stdout").
func events(t testing.TB, stdout string) []event {
	t.Helper()
	var out []event
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	for sc.Scan() {
		n++
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Errorf("stdout line %d is not a JSON object: %q (%v)", n, sc.Text(), err)
			continue
		}
		out = append(out, e)
	}
	return out
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }
