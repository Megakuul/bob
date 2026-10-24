// Command budgetbench measures the budget rows of tests/budgets/budgets.json that the product
// can measure on this host, for the budgets stage of tools/ci (HARNESS.md §12). Each group
// prints one "BUDGET <row id> <value>" line per row it measured (value in the row's unit and
// statistic), informational lines starting with "#", and writes a result file
// out/budgets/<group>.json labelled with the machine and the load average (HARNESS §12.2).
//
//	go run ./tools/budgetbench <group>
//
// Groups (rows):
//
//	noop-go        B-016, B-021 (no-op `bob build //...` of fixture go, median of 5), B-009
//	               (namespace entry, median of the same runs' `namespace` event)
//	noop-polyglot  B-007 (no-op `bob build //...` of fixture polyglot, median of 5)
//	noop-cc        B-053;  noop-rust B-029;  noop-ocaml B-045
//	codegen        B-049 (`bob gen` no-op), B-052 (`bob status`), fixture codegen
//	sizes          B-010 (closure NAR of //cmd/api:api), B-011 (its bin/api), fixture polyglot
//	appimage       B-087 (AppImage file size of //deploy:api-appimage), fixture polyglot
//	eval1000       B-001 (evaluation of 1,000 bob.go files, eval key hit, `eval` event)
//	evalbox        B-075 (evaluator sandbox local defaults)
//	ui             B-097 (initial JS and CSS of web/build, gzip)
//	npm-lock       B-037 (`bob lock` with inputs unchanged, fixture npm, median of 5)
//	dev            B-079, B-019 (hook fast path p95), B-080/B-018 (shim start: bob exec -- /bin/true, p95 /
//	               median of 100), B-014 (bob dev -- true, median of 5), B-081 (warm definition
//	               through shim + LSP proxy, mean of 20): tools/devbench -budget on fixture go
//	api            B-070 (API unary p99: GetProject on the dev stack; ListProjects ≤ 150 ms checked),
//	               B-068 (narinfo p50 with a warm visibility cache; p99 ≤ 20 ms checked)
//	fleet-jobs     B-057, B-088 (requests per job with wake-ups, 300 jobs, 20 workers; 0 LIST)
//	fleet-idle     B-058 (requests per idle worker and second, 20 workers, 60 s window)
//
// Fixtures are copied from tests/fixtures/repos into the work directory (-work, default
// $BOB_BENCH_DIR or ${XDG_CACHE_HOME:-~/.cache}/bob-budgetbench), committed to a fresh git
// repository and built once (untimed) in the work directory's own bob home. Later runs reuse
// that state, so only the first run of a group pays the cold build. Every timed measurement
// starts with one untimed warm-up run (a rebuilt bob has a new evaluation key). bob itself is
// built from this repository (CGO_ENABLED=0) into <work>/bin on every run.
//
// The budgets' [8c/15G] profile is a quiet host; a 1-minute load average above 1.0 is printed
// as "busy" and the result file says "measured under load".
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
)

func main() {
	work := flag.String("work", defaultWork(), "work directory (fixture copies, bob home, binaries)")
	runs := flag.Int("runs", 5, "timed runs per measurement (after one warm-up)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: budgetbench [-work dir] [-runs n] <group>")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *work, *runs); err != nil {
		fmt.Fprintln(os.Stderr, "budgetbench:", err)
		os.Exit(1)
	}
}

func defaultWork() string {
	if d := os.Getenv("BOB_BENCH_DIR"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "bob-budgetbench")
	}
	return filepath.Join(os.TempDir(), "bob-budgetbench")
}

// bench is one group's run.
type bench struct {
	root, work, bob string
	runs            int
	res             result
}

// result is out/budgets/<group>.json.
type result struct {
	Group      string         `json:"group"`
	Time       string         `json:"time"`
	Host       string         `json:"host"`
	LoadBefore string         `json:"loadBefore"`
	LoadAfter  string         `json:"loadAfter"`
	Conditions string         `json:"conditions"`
	Rows       map[string]row `json:"rows"`
	Notes      []string       `json:"notes,omitempty"`
}

type row struct {
	Value   float64   `json:"value"`
	Unit    string    `json:"unit"`
	Stat    string    `json:"stat"`
	Samples []float64 `json:"samples,omitempty"`
}

func run(group, work string, runs int) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "tests", "budgets", "budgets.json")); err != nil {
		return errors.New("run from the repository root")
	}
	if work, err = filepath.Abs(work); err != nil {
		return err
	}
	b := &bench{root: root, work: work, runs: max(runs, 1)}
	b.res = result{Group: group, Time: time.Now().UTC().Format(time.RFC3339), Host: host(),
		LoadBefore: loadavg(), Rows: map[string]row{}}
	b.note("host %s, load %s", b.res.Host, b.res.LoadBefore)
	groups := map[string]func() error{
		"noop-go":       func() error { return b.noop("go", "B-016", "B-021") },
		"noop-polyglot": func() error { return b.noop("polyglot", "B-007") },
		"noop-cc":       func() error { return b.noop("cc", "B-053") },
		"noop-rust":     func() error { return b.noop("rust", "B-029") },
		"noop-ocaml":    func() error { return b.noop("ocaml", "B-045") },
		"codegen":       b.codegen,
		"sizes":         b.sizes,
		"appimage":      b.appimage,
		"eval1000":      b.eval1000,
		"evalbox":       b.evalbox,
		"ui":            b.ui,
		"dev":           b.dev,
		"npm-lock":      b.npmLock,
		"api":           b.api,
		"fleet-jobs":    b.fleetJobs,
		"fleet-idle":    b.fleetIdle,
	}
	f, ok := groups[group]
	if !ok {
		names := make([]string, 0, len(groups))
		for n := range groups {
			names = append(names, n)
		}
		slices.Sort(names)
		return fmt.Errorf("unknown group %q (have %s)", group, strings.Join(names, ", "))
	}
	if !strings.HasPrefix(group, "fleet-") && group != "api" && group != "evalbox" && group != "ui" {
		if err := b.buildBob(); err != nil {
			return err
		}
	}
	err = f()
	b.res.LoadAfter = loadavg()
	b.res.Conditions = "quiet"
	if loadOf(b.res.LoadBefore) > 1 || loadOf(b.res.LoadAfter) > 1 {
		b.res.Conditions = "measured under load (1-minute load average > 1.0; budgets assume a quiet host)"
	}
	b.note("load after %s: %s", b.res.LoadAfter, b.res.Conditions)
	if werr := b.write(); werr != nil && err == nil {
		err = werr
	}
	return err
}

// buildBob builds bob and bob-builder from the repository into <work>/bin.
func (b *bench) buildBob() error {
	bin := filepath.Join(b.work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	for _, n := range []string{"bob", "bob-builder"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, n), "./cmd/"+n)
		cmd.Dir = b.root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build ./cmd/%s: %w\n%s", n, err, out)
		}
	}
	b.bob = filepath.Join(bin, "bob")
	return nil
}

// env is the minimal environment of every bob run (as the acceptance harness): a fixed HOME and
// bob home in the work directory, so the store and caches survive between runs.
func (b *bench) env() []string {
	home := filepath.Join(b.work, "home")
	tmp := filepath.Join(b.work, "tmp")
	_ = os.MkdirAll(home, 0o755)
	_ = os.MkdirAll(tmp, 0o755)
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "BOB_HOME=" + b.bobHome(), "TMPDIR=" + tmp,
		"TZ=UTC", "LC_ALL=C"}
}

func (b *bench) bobHome() string { return filepath.Join(b.work, "bobhome") }

// bobRun runs bob in dir and returns stdout, the wall time and an error with stderr.
func (b *bench) bobRun(dir string, args ...string) ([]byte, time.Duration, error) {
	cmd := exec.Command(b.bob, args...)
	cmd.Dir, cmd.Env = dir, b.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	t0 := time.Now()
	err := cmd.Run()
	d := time.Since(t0)
	if err != nil {
		return out.Bytes(), d, fmt.Errorf("bob %s in %s: %w\n%s%s", strings.Join(args, " "), dir, err,
			tail(out.String(), 2000), tail(errb.String(), 4000))
	}
	return out.Bytes(), d, nil
}

// fixture returns the prepared copy of tests/fixtures/repos/<name>: copied and committed on
// first use, then built once with `bob build //...` (untimed) until that succeeds.
func (b *bench) fixture(name string) (string, error) {
	dir := filepath.Join(b.work, "repos", name)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		_ = os.RemoveAll(dir)
		if err := copyTree(filepath.Join(b.root, "tests", "fixtures", "repos", name), dir); err != nil {
			return "", fmt.Errorf("copy fixture %s: %w", name, err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"},
			{"-c", "user.name=budgetbench", "-c", "user.email=bench@bob.invalid", "-c", "gc.auto=0",
				"-c", "maintenance.auto=false", "commit", "-q", "-m", "fixture"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git %v: %w: %s", args, err, out)
			}
		}
	}
	stamp := filepath.Join(b.work, "repos", name+".built")
	if _, err := os.Stat(stamp); err != nil {
		b.note("preparing fixture %s: cold bob build //... (untimed, once per work directory)", name)
		t0 := time.Now()
		if _, _, err := b.bobRun(dir, "build", "//..."); err != nil {
			return "", fmt.Errorf("prepare %s: %w", name, err)
		}
		b.note("fixture %s prepared in %.1f s", name, time.Since(t0).Seconds())
		if err := os.WriteFile(stamp, nil, 0o644); err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(dir) // bob names projects by their real path
}

// timed runs f once untimed (warm true: it may build or evaluate), then b.runs times, and
// returns the wall times in seconds.
func (b *bench) timed(f func(warm bool) (time.Duration, error)) ([]float64, error) {
	if _, err := f(true); err != nil {
		return nil, err
	}
	var s []float64
	for range b.runs {
		d, err := f(false)
		if err != nil {
			return nil, err
		}
		s = append(s, d.Seconds())
	}
	return s, nil
}

// noop measures `bob build //...` with nothing changed on fixture name for the given rows (all
// the same measurement); fixture go also yields B-009 from the runs' namespace events.
func (b *bench) noop(name string, rows ...string) error {
	dir, err := b.fixture(name)
	if err != nil {
		return err
	}
	var ns []float64
	s, err := b.timed(func(warm bool) (time.Duration, error) {
		out, d, err := b.bobRun(dir, "build", "--json", "//...")
		if err != nil || warm {
			return d, err
		}
		evs := events(out)
		if fin := named(evs, "run.finish"); len(fin) == 1 {
			if c, _ := fin[0]["counts"].(map[string]any); c != nil && num(c["built"]) > 0 {
				return d, fmt.Errorf("the no-op build of %s built %v derivations", name, c["built"])
			}
		}
		if e := named(evs, "namespace"); len(e) == 1 {
			ns = append(ns, num(e[0]["durationMs"]))
		}
		return d, nil
	})
	if err != nil {
		return err
	}
	for _, id := range rows {
		b.budget(id, median(s), "s", "median", s)
	}
	if name == "go" && len(ns) > 0 {
		b.budget("B-009", median(ns), "ms", "median", ns)
	}
	return nil
}

// dev runs tools/devbench -budget on fixture go after `bob allow` and one `bob dev -- true`
// (the prepared project devbench expects), with the same HOME and bob home as every run.
func (b *bench) dev() error {
	dir, err := b.fixture("go")
	if err != nil {
		return err
	}
	for _, args := range [][]string{{"allow"}, {"dev", "--", "true"}} {
		if _, _, err := b.bobRun(dir, args...); err != nil {
			return err
		}
	}
	devbench := filepath.Join(b.work, "bin", "devbench")
	cmd := exec.Command("go", "build", "-o", devbench, "./tools/devbench")
	cmd.Dir = b.root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build ./tools/devbench: %w\n%s", err, out)
	}
	cmd = exec.Command(devbench, "-budget", "-bob", b.bob, "-dir", dir, "-runs", "100", "-dev-runs", strconv.Itoa(b.runs),
		"-lsp-file", "api/cmd/api/main.go", "-lsp-needle", "fmt.Printf", "-only", "hook,exec,dev,lsp")
	cmd.Dir, cmd.Env = dir, b.env()
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	units := map[string][2]string{"B-079": {"ms", "p95"}, "B-019": {"ms", "p95"}, "B-080": {"ms", "p95"}, "B-018": {"ms", "median"},
		"B-014": {"s", "median"}, "B-081": {"ms", "mean"}}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 && f[0] == "BUDGET" {
			v, perr := strconv.ParseFloat(f[2], 64)
			if perr != nil {
				return fmt.Errorf("devbench: bad line %q", l)
			}
			u := units[f[1]]
			b.budget(f[1], v, u[0], u[1], nil)
			continue
		}
		if l != "" {
			b.note("devbench: %s", l)
		}
	}
	if err != nil {
		return fmt.Errorf("devbench: %w\n%s", err, tail(errb.String(), 4000))
	}
	return nil
}

// npmLock measures `bob lock` with nothing changed on fixture npm (B-037). The warm-up run
// writes whatever the first lock of the copy changes; the timed runs must report no change.
func (b *bench) npmLock() error {
	dir, err := b.fixture("npm")
	if err != nil {
		return err
	}
	s, err := b.timed(func(warm bool) (time.Duration, error) {
		out, d, err := b.bobRun(dir, "lock", "--json")
		if err != nil || warm {
			return d, err
		}
		if fin := named(events(out), "run.finish"); len(fin) != 1 || num(fin[0]["exitCode"]) != 0 {
			return d, fmt.Errorf("bob lock --json: no successful run.finish:\n%s", tail(string(out), 2000))
		}
		return d, nil
	})
	if err != nil {
		return err
	}
	b.budget("B-037", median(s), "s", "median", s)
	return nil
}

// codegen measures `bob gen` and `bob status` with nothing changed on fixture codegen.
func (b *bench) codegen() error {
	dir, err := b.fixture("codegen")
	if err != nil {
		return err
	}
	for _, m := range []struct {
		id   string
		args []string
	}{{"B-049", []string{"gen"}}, {"B-052", []string{"status"}}} {
		s, err := b.timed(func(bool) (time.Duration, error) {
			_, d, err := b.bobRun(dir, m.args...)
			return d, err
		})
		if err != nil {
			return err
		}
		b.budget(m.id, median(s), "s", "median", s)
	}
	return nil
}

// buildPath builds label in dir and returns the host path of its first output.
func (b *bench) buildPath(dir, label string) (string, error) {
	out, _, err := b.bobRun(dir, "build", "--print-paths", "--json", label)
	if err != nil {
		return "", err
	}
	plain, _, _ := strings.Cut(label, "@")
	for _, e := range named(events(out), "package.finish") {
		if l, _ := e["label"].(string); l != plain {
			continue
		}
		if ps, _ := e["paths"].([]any); len(ps) > 0 {
			if p, _ := ps[0].(string); p != "" {
				if strings.HasPrefix(p, "/nix/store/") {
					p = filepath.Join(b.bobHome(), p) // a store path, not yet a host path
				}
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("bob build %s printed no output path", label)
}

const mib = 1 << 20

// sizes measures the closure (NAR) of //cmd/api:api and its binary (fixture polyglot).
func (b *bench) sizes() error {
	dir, err := b.fixture("polyglot")
	if err != nil {
		return err
	}
	p, err := b.buildPath(dir, "//cmd/api:api")
	if err != nil {
		return err
	}
	size, n, err := b.closure(p)
	if err != nil {
		return err
	}
	b.budget("B-010", float64(size)/mib, "MiB", "value", nil)
	b.note("closure of //cmd/api:api: %d paths (budget 12)", n)
	if n > 12 {
		b.res.Notes = append(b.res.Notes, fmt.Sprintf("B-010 path count %d > 12", n))
	}
	fi, err := os.Stat(filepath.Join(p, "bin", "api"))
	if err != nil {
		return err
	}
	b.budget("B-011", float64(fi.Size())/mib, "MiB", "value", nil)
	return nil
}

// closure returns the NAR size and path count of a store path's closure, read with the
// embedded Nix of the bob home.
func (b *bench) closure(hostPath string) (int64, int, error) {
	home, err := filepath.EvalSymlinks(b.bobHome()) // Nix refuses a store below a symlink
	if err != nil {
		return 0, 0, err
	}
	nixes, _ := filepath.Glob(filepath.Join(home, "bin", "nix-*", "bin", "nix"))
	if len(nixes) == 0 {
		return 0, 0, errors.New("no nix below the bob home")
	}
	cmd := exec.Command(nixes[0], "--extra-experimental-features", "nix-command", "--store", home,
		"path-info", "--json", "--json-format", "1", "-r", "/nix/store/"+filepath.Base(hostPath))
	cmd.Env = append(b.env(), "HOME="+filepath.Join(b.work, "tmp"))
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("nix path-info: %w: %s", err, tail(errb.String(), 2000))
	}
	var info map[string]struct {
		NarSize int64 `json:"narSize"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, 0, err
	}
	var total int64
	for _, i := range info {
		total += i.NarSize
	}
	return total, len(info), nil
}

// appimage measures the AppImage of //deploy:api-appimage (fixture polyglot), in MB (10^6).
func (b *bench) appimage() error {
	dir, err := b.fixture("polyglot")
	if err != nil {
		return err
	}
	p, err := b.buildPath(dir, "//deploy:api-appimage")
	if err != nil {
		return err
	}
	file := p
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		ms, _ := filepath.Glob(filepath.Join(p, "*.AppImage"))
		if len(ms) != 1 {
			return fmt.Errorf("%s holds %d .AppImage files, want 1", p, len(ms))
		}
		file = ms[0]
	}
	fi, err := os.Stat(file)
	if err != nil {
		return err
	}
	b.budget("B-087", float64(fi.Size())/1e6, "MB", "value", nil)
	return nil
}

// eval1000 measures the evaluation of a generated repository with 1,000 bob.go files when
// nothing changed (eval key hit): the `eval` event of `bob graph --json`, median of the runs.
func (b *bench) eval1000() error {
	dir, err := b.synthetic1000()
	if err != nil {
		return err
	}
	var ms []float64
	_, err = b.timed(func(warm bool) (time.Duration, error) {
		out, d, err := b.bobRun(dir, "graph", "--json")
		if err != nil || warm {
			return d, err
		}
		e := named(events(out), "eval")
		if len(e) != 1 {
			return d, fmt.Errorf("bob graph --json printed %d eval events, want 1", len(e))
		}
		if c, _ := e[0]["cached"].(bool); !c {
			return d, errors.New("evaluation was not a key hit with nothing changed")
		}
		ms = append(ms, num(e[0]["durationMs"])/1000)
		return d, nil
	})
	if err != nil {
		return err
	}
	b.budget("B-001", median(ms), "s", "median", ms)
	return nil
}

// synthetic1000 writes (once) a repository with 1,000 bob.go files: the root one with the
// repository options and 999 directories with one trivial bob.Run step each (the shape of the
// R-FW-020 chain repository of tests/core).
func (b *bench) synthetic1000() (string, error) {
	dir := filepath.Join(b.work, "repos", "eval1000")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	write := func(rel, s string) error {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(s), 0o644)
	}
	if err := write("go.mod", "module example.com/big\n\ngo 1.26.0\n"); err != nil {
		return "", err
	}
	if err := write("bob.lock", "{\n  \"lockVersion\": 1\n}\n"); err != nil {
		return "", err
	}
	if err := write("bob.go", "//go:build bob\n\npackage big\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\n"+
		"var _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n"); err != nil {
		return "", err
	}
	for i := range 999 {
		pkg := fmt.Sprintf("p%03d", i)
		rel := fmt.Sprintf("steps/g%02d/%s", i/40, pkg)
		if err := write(rel+"/bob.go", fmt.Sprintf(`//go:build bob

package %s

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var step = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "s", bob.RunOpts{Tools: []bob.Tool{nixpkgs.Tool(p, "coreutils")}, Cmd: []string{"true"}, Env: map[string]string{"N": "%d"}})
})
`, pkg, i)); err != nil {
			return "", err
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=budgetbench", "-c", "user.email=bench@bob.invalid", "-c", "gc.auto=0",
			"-c", "maintenance.auto=false", "commit", "-q", "-m", "synthetic"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %v: %w: %s", args, err, out)
		}
	}
	// Pin nixpkgs in bob.lock (as every real repository has it): without a pin each
	// evaluation asks the channel for its revision over the network.
	if _, _, err := b.bobRun(dir, "lock"); err != nil {
		return "", err
	}
	return dir, nil
}

// evalbox reads the evaluator sandbox's local defaults where the local build takes them
// (build passes zero limits, so evalbox.Limits.WithDefaults applies) and checks every clause of
// the row: wall clock 120 s, IR 64 MiB, stderr 1 MiB, pids 512, memory 2 GiB.
func (b *bench) evalbox() error {
	l := evalbox.Limits{}.WithDefaults()
	var bad []string
	for _, c := range []struct {
		name      string
		got, want int64
	}{
		{"wall clock (s)", int64(l.WallClock / time.Second), 120},
		{"IR output (bytes)", l.IROutput, 64 << 20},
		{"stderr kept (bytes)", l.StderrKept, 1 << 20},
		{"pids", int64(l.Pids), 512},
		{"memory (bytes)", l.MemoryBytes, 2 << 30},
	} {
		b.note("evaluator default %s = %d (SPEC R-FW-045: %d)", c.name, c.got, c.want)
		if c.got > c.want {
			bad = append(bad, fmt.Sprintf("%s %d > %d", c.name, c.got, c.want))
		}
	}
	b.budget("B-075", l.WallClock.Seconds(), "s", "median", nil)
	if len(bad) > 0 {
		return fmt.Errorf("evaluator defaults exceed SPEC R-FW-045: %s", strings.Join(bad, ", "))
	}
	return nil
}

// ui measures the first load of web/build: the gzip sizes of the JavaScript and CSS that the
// SPA entry page 200.html loads (its <script>/<link> references and the static imports of those
// modules, transitively). JS is the row's value; CSS over 30 KiB fails the group.
func (b *bench) ui() error {
	build := filepath.Join(b.root, "web", "build")
	page, err := os.ReadFile(filepath.Join(build, "200.html"))
	if err != nil {
		return fmt.Errorf("web/build/200.html: %w (build the UI first)", err)
	}
	if fi, err := os.Stat(filepath.Join(build, "200.html")); err == nil {
		if newer := newestUnder(filepath.Join(b.root, "web", "src")); newer.After(fi.ModTime()) {
			b.note("warning: web/src changed after web/build was written (%s > %s): sizes are of the older build",
				newer.UTC().Format(time.RFC3339), fi.ModTime().UTC().Format(time.RFC3339))
		}
	}
	refRe := regexp.MustCompile(`(?:href|src)="([^"]+\.(?:js|css))"|import\("([^"]+\.js)"\)`)
	importRe := regexp.MustCompile(`(?:import|from)\s*["']([^"']+\.js)["']|import\(["'](\./[^"']+\.js)["']\)`)
	seen := map[string]bool{}
	var queue []string
	add := func(base, ref string) {
		var p string
		switch {
		case strings.HasPrefix(ref, "/"):
			p = filepath.Join(build, filepath.FromSlash(ref))
		default:
			p = filepath.Join(base, filepath.FromSlash(ref))
		}
		if !seen[p] {
			seen[p] = true
			queue = append(queue, p)
		}
	}
	for _, m := range refRe.FindAllStringSubmatch(string(page), -1) {
		add(build, m[1]+m[2])
	}
	var js, css int64
	var nJS, nCSS int
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("first-load asset: %w", err)
		}
		gz, err := gzSize(data)
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".css") {
			css, nCSS = css+gz, nCSS+1
			continue
		}
		js, nJS = js+gz, nJS+1
		// Static imports load with the page; dynamic import() of page modules is the router's
		// first page, which the SPA entry loads at start too.
		for _, m := range importRe.FindAllStringSubmatch(string(data), -1) {
			if r := m[1] + m[2]; strings.HasPrefix(r, ".") || strings.HasPrefix(r, "/") {
				add(filepath.Dir(p), r)
			}
		}
	}
	if nJS == 0 {
		return errors.New("200.html references no JavaScript")
	}
	b.budget("B-097", float64(js)/1024, "KiB", "value", nil)
	b.note("first load: %d JS files %.1f KiB gz (budget 250), %d CSS files %.1f KiB gz (budget 30); FCP not measured (no browser in the bench)",
		nJS, float64(js)/1024, nCSS, float64(css)/1024)
	if float64(css)/1024 > 30 {
		return fmt.Errorf("first-load CSS %.1f KiB gzip > 30 KiB (B-097)", float64(css)/1024)
	}
	return nil
}

func gzSize(data []byte) (int64, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(data); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return int64(buf.Len()), nil
}

func newestUnder(dir string) time.Time {
	var t time.Time
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.ModTime().After(t) {
				t = fi.ModTime()
			}
		}
		return nil
	})
	return t
}

// budget prints the BUDGET line of a row and records it in the result file.
func (b *bench) budget(id string, v float64, unit, stat string, samples []float64) {
	fmt.Printf("BUDGET %s %s\n", id, strconv.FormatFloat(v, 'f', 4, 64))
	ss := ""
	if len(samples) > 0 {
		parts := make([]string, len(samples))
		for i, s := range samples {
			parts[i] = strconv.FormatFloat(s, 'f', 3, 64)
		}
		ss = " samples [" + strings.Join(parts, " ") + "]"
	}
	b.note("%s %s %.4f %s%s (load %s)", id, stat, v, unit, ss, loadavg())
	b.res.Rows[id] = row{Value: v, Unit: unit, Stat: stat, Samples: samples}
}

func (b *bench) note(format string, args ...any) {
	fmt.Printf("# "+format+"\n", args...)
}

func (b *bench) write() error {
	dir := filepath.Join(b.root, "out", "budgets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b.res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, b.res.Group+".json"), append(data, '\n'), 0o644)
}

func events(out []byte) []map[string]any {
	var evs []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			evs = append(evs, e)
		}
	}
	return evs
}

func named(evs []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		if e["event"] == name {
			out = append(out, e)
		}
	}
	return out
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func median(s []float64) float64 {
	c := slices.Clone(s)
	slices.Sort(c)
	if len(c) == 0 {
		return 0
	}
	if len(c)%2 == 1 {
		return c[len(c)/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, out)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, info.Mode().Perm())
	})
}

func host() string {
	h, _ := os.Hostname()
	return fmt.Sprintf("%s, %d CPUs, %s/%s", h, runtime.NumCPU(), runtime.GOOS, runtime.GOARCH)
}

func loadavg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "?"
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return "?"
	}
	return strings.Join(f[:3], " ")
}

func loadOf(s string) float64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
