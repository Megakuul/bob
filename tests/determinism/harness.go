// Package determinism is the determinism and reproducibility suite (HARNESS.md §6).
//
// It drives the bob binary as a black box: every fixture repository is evaluated twice in two
// copies at different absolute paths and with a different environment, and every fixture output
// is built twice by two bob processes with separate state directories (and so separate stores).
// Evaluations must print identical bytes; outputs must have identical NAR hashes. A deviating
// output is listed file by file in out/ci/<run>/determinism/ and fails unless it is in
// known-deviations.json. Cross-machine reproducibility is (manual) until an x86_64 runner exists
// (HARNESS.md §6.5, D66).
package determinism

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// EnvRun names the run directory below out/ci; tools/ci sets it. Default "local".
const EnvRun = "BOB_CI_RUN"

const notImplemented = "BOB-E-9008"

// Status is the outcome of one check.
type Status string

const (
	Pass Status = "pass"
	Red  Status = "red" // bob answered BOB-E-9008: the feature is not built yet
	Fail Status = "fail"
)

// Fixtures returns the fixture repositories below dir: every directory holding a bob.lock. A
// missing dir is no error, so the suite runs before the fixtures exist.
func Fixtures(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fixtures: %w", err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "bob.lock")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// CopyTree copies the regular files, directories and symlinks of src to dst.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case info.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, to)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(to, b, info.Mode().Perm())
		}
		return nil
	})
}

// Worker is one bob process environment: its own HOME, XDG dirs (and so its own store under
// $XDG_DATA_HOME/bob/nix/store), time zone, locale and environment order.
type Worker struct {
	Bin     string
	Home    string
	TZ      string
	Lang    string
	Reverse bool // pass the environment in reverse order
}

// Env returns the worker's process environment.
func (w Worker) Env() []string {
	env := []string{
		"HOME=" + w.Home,
		"XDG_DATA_HOME=" + filepath.Join(w.Home, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(w.Home, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(w.Home, "config"),
		"XDG_STATE_HOME=" + filepath.Join(w.Home, "state"),
		"TZ=" + w.TZ,
		"LANG=" + w.Lang,
		"LC_ALL=" + w.Lang,
		"PATH=" + os.Getenv("PATH"),
		"NO_COLOR=1",
	}
	if w.Reverse {
		slices.Reverse(env)
	}
	return env
}

// Store returns the host directory of the worker's store (SPEC §02.7.5).
func (w Worker) Store() string { return filepath.Join(w.Home, "data", "bob", "nix", "store") }

// Output is one bob invocation.
type Output struct {
	Args           []string
	Stdout, Stderr []byte
	Exit           int
}

var panicRE = regexp.MustCompile(`(?m)^(panic: |fatal error: |goroutine \d+ \[)`)

// Classify sorts an invocation into pass, red (not implemented) or fail with a reason.
func (o Output) Classify() (Status, string) {
	all := append(append([]byte{}, o.Stdout...), o.Stderr...)
	switch {
	case panicRE.Match(all):
		return Fail, fmt.Sprintf("%s crashed:\n%s", strings.Join(o.Args, " "), all)
	case o.Exit == 0:
		return Pass, ""
	case bytes.Contains(all, []byte(notImplemented)) || bytes.Contains(all, []byte(`"code":9008`)):
		return Red, fmt.Sprintf("%s: not implemented", strings.Join(o.Args, " "))
	}
	return Fail, fmt.Sprintf("%s exited %d:\n%s", strings.Join(o.Args, " "), o.Exit, all)
}

// Run runs bob with args in dir.
func (w Worker) Run(ctx context.Context, dir string, args ...string) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Bin, args...)
	cmd.Dir = dir
	cmd.Env = w.Env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	o := Output{Args: append([]string{"bob"}, args...)}
	err := cmd.Run()
	o.Stdout, o.Stderr = stdout.Bytes(), stderr.Bytes()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		o.Exit = ee.ExitCode()
	default:
		return o, fmt.Errorf("run %s: %w", strings.Join(o.Args, " "), err)
	}
	return o, nil
}

// PrintedPaths returns the host output paths of the package.finish events of a --json
// --print-paths run, keyed by store path base name (hash-name).
func PrintedPaths(stdout []byte) (map[string]string, error) {
	paths := map[string]string{}
	for line := range bytes.SplitSeq(stdout, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev struct {
			Event string   `json:"event"`
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("--json line %q: %w", line, err)
		}
		if ev.Event != "package.finish" {
			continue
		}
		for _, p := range ev.Paths {
			paths[filepath.Base(p)] = p
		}
	}
	return paths, nil
}

// NarHash returns the NAR sha256 of the tree at path. `nix hash path` serialises any directory
// as a NAR, so it works on a worker's host store path without that store being registered.
func NarHash(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "nix", "--extra-experimental-features", "nix-command",
		"hash", "path", "--type", "sha256", "--base32", path).Output()
	if err != nil {
		return "", fmt.Errorf("nix hash path %s: %w", path, err)
	}
	return "sha256:" + strings.TrimSpace(string(out)), nil
}

// FileDiff is one differing file of a deviating output.
type FileDiff struct {
	File   string `json:"file"`
	Kind   string `json:"kind"`             // only-a, only-b, type, mode, content, symlink
	Offset int64  `json:"offset,omitempty"` // first differing byte (content)
	A      string `json:"a,omitempty"`      // up to 16 bytes from Offset, hex
	B      string `json:"b,omitempty"`
}

// DiffTrees lists how the trees a and b differ, in path order (a diffoscope-style summary).
func DiffTrees(a, b string) ([]FileDiff, error) {
	la, err := listTree(a)
	if err != nil {
		return nil, err
	}
	lb, err := listTree(b)
	if err != nil {
		return nil, err
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range la {
			if !yield(n) {
				return
			}
		}
		for n := range lb {
			if _, ok := la[n]; !ok && !yield(n) {
				return
			}
		}
	})
	var diffs []FileDiff
	for _, n := range names {
		ia, okA := la[n]
		ib, okB := lb[n]
		switch {
		case !okB:
			diffs = append(diffs, FileDiff{File: n, Kind: "only-a"})
		case !okA:
			diffs = append(diffs, FileDiff{File: n, Kind: "only-b"})
		case ia.Mode().Type() != ib.Mode().Type():
			diffs = append(diffs, FileDiff{File: n, Kind: "type"})
		case ia.Mode().IsRegular() && ia.Mode().Perm()&0o111 != ib.Mode().Perm()&0o111:
			diffs = append(diffs, FileDiff{File: n, Kind: "mode"})
		case ia.Mode()&fs.ModeSymlink != 0:
			ta, _ := os.Readlink(filepath.Join(a, n))
			tb, _ := os.Readlink(filepath.Join(b, n))
			if ta != tb {
				diffs = append(diffs, FileDiff{File: n, Kind: "symlink", A: ta, B: tb})
			}
		case ia.Mode().IsRegular():
			d, err := diffFile(filepath.Join(a, n), filepath.Join(b, n))
			if err != nil {
				return nil, err
			}
			if d != nil {
				d.File = n
				diffs = append(diffs, *d)
			}
		}
	}
	return diffs, nil
}

func listTree(root string) (map[string]fs.FileInfo, error) {
	m := map[string]fs.FileInfo{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		m[rel] = info
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", root, err)
	}
	return m, nil
}

func diffFile(a, b string) (*FileDiff, error) {
	fa, err := os.Open(a)
	if err != nil {
		return nil, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return nil, err
	}
	defer fb.Close()
	const chunk = 64 << 10
	ba, bb := make([]byte, chunk), make([]byte, chunk)
	var off int64
	for {
		na, errA := io.ReadFull(fa, ba)
		nb, errB := io.ReadFull(fb, bb)
		n := min(na, nb)
		for i := range n {
			if ba[i] != bb[i] {
				return &FileDiff{Kind: "content", Offset: off + int64(i),
					A: fmt.Sprintf("%x", ba[i:min(i+16, na)]), B: fmt.Sprintf("%x", bb[i:min(i+16, nb)])}, nil
			}
		}
		if na != nb {
			return &FileDiff{Kind: "content", Offset: off + int64(n)}, nil
		}
		off += int64(n)
		endA := errA == io.EOF || errors.Is(errA, io.ErrUnexpectedEOF)
		endB := errB == io.EOF || errors.Is(errB, io.ErrUnexpectedEOF)
		if errA != nil && !endA {
			return nil, errA
		}
		if errB != nil && !endB {
			return nil, errB
		}
		if endA || endB {
			return nil, nil
		}
	}
}

// Deviation is one output whose NAR hash differs between the two workers.
type Deviation struct {
	Fixture string     `json:"fixture"`
	Path    string     `json:"path"` // store path base name
	NarA    string     `json:"narA"`
	NarB    string     `json:"narB"`
	Files   []FileDiff `json:"files"`
	Known   string     `json:"known,omitempty"` // reason from known-deviations.json
}

// Known is tests/determinism/known-deviations.json (protected).
type Known struct {
	V          int `json:"v"`
	Deviations []struct {
		Fixture string `json:"fixture"`
		Name    string `json:"name"` // store path name without hash, e.g. "acme-api-1.0"
		Reason  string `json:"reason"`
		Owner   string `json:"owner"` // sign-off
	} `json:"deviations"`
}

// LoadKnown reads the known deviations file.
func LoadKnown(path string) (Known, error) {
	var k Known
	b, err := os.ReadFile(path)
	if err != nil {
		return k, fmt.Errorf("known deviations: %w", err)
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return k, fmt.Errorf("known deviations %s: %w", path, err)
	}
	return k, nil
}

// Reason returns the recorded reason when the output is a known deviation.
func (k Known) Reason(fixture, base string) string {
	_, name, _ := strings.Cut(base, "-")
	for _, d := range k.Deviations {
		if d.Fixture == fixture && d.Name == name && d.Owner != "" {
			return d.Reason
		}
	}
	return ""
}

// Diffoscope runs diffoscope on a deviating pair when it is on PATH and returns its text
// report; it is optional (HARNESS.md §6.3 asks for a diffoscope-style listing, which DiffTrees
// always gives).
func Diffoscope(ctx context.Context, a, b string) []byte {
	bin, err := exec.LookPath("diffoscope")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "--text", "-", a, b).Output()
	return out
}
