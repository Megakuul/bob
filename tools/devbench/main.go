// Command devbench measures bob's developer-environment start-up paths on this host (budgets
// B-014, B-018, B-079, B-080, B-081, B-083; SPEC R-DEV-006/007/008/010):
//
//   - hook:  `bob env --hook=bash` on the fast path (the shell state of an entered project);
//   - exec:  `bob exec --project <dir> -- /bin/true` (what every shim runs);
//   - shim:  `.bob/bin/<tool> <args>` (sh script → bob exec → tool), including the tool's own
//     run time (`-shim "go version"` by default);
//   - dev:   `bob dev -- true` with nothing changed (median of -dev-runs);
//   - lsp:   warm textDocument/definition through `.bob/bin/gopls` (shim + LSP proxy), mean.
//
// The project must be a Go project whose default dev shell has been prepared (`bob allow` and
// one `bob dev -- true` in the same HOME). Load averages before and after are printed, because
// timings on a loaded host are not comparable with the budgets' quiet [8c/15G] profile.
// With -budget it also prints "BUDGET <row> <value>" lines for the budgets stage (tools/ci,
// tools/budgetbench group dev): B-079 and B-019 hook p95 (ms), B-080 exec p95 and B-018 exec median (ms),
// B-014 dev median (s), B-081 warm definition mean (ms).
//
//	go run ./tools/devbench -bob /path/to/bob -dir /path/to/project [-runs 100]
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// budgetLines makes report print BUDGET lines (-budget).
var budgetLines bool

func main() {
	bob := flag.String("bob", "bob", "bob executable")
	dir := flag.String("dir", ".", "prepared project root")
	runs := flag.Int("runs", 100, "runs per start-up measurement")
	devRuns := flag.Int("dev-runs", 5, "runs of bob dev -- true")
	shimArgs := flag.String("shim", "go version", "shim tool and arguments (.bob/bin/<tool>)")
	lspFile := flag.String("lsp-file", "cmd/hello/main.go", "Go file for the LSP measurement")
	lspNeedle := flag.String("lsp-needle", "fmt.Println", "definition target in -lsp-file (at +5)")
	only := flag.String("only", "hook,exec,shim,dev,lsp", "measurements to run")
	flag.BoolVar(&budgetLines, "budget", false, "print BUDGET lines for the budgets stage")
	flag.Parse()
	if err := run(*bob, *dir, *runs, *devRuns, *shimArgs, *lspFile, *lspNeedle, *only); err != nil {
		fmt.Fprintln(os.Stderr, "devbench:", err)
		os.Exit(1)
	}
}

func run(bob, dir string, runs, devRuns int, shimArgs, lspFile, lspNeedle, only string) error {
	var err error
	if bob, err = exec.LookPath(bob); err != nil {
		return err
	}
	if bob, err = filepath.Abs(bob); err != nil {
		return err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	fmt.Printf("host: %d CPUs, %s/%s, load %s\n", runtime.NumCPU(), runtime.GOOS, runtime.GOARCH, load())
	want := strings.Split(only, ",")
	if slices.Contains(want, "hook") {
		if err := benchHook(bob, dir, runs); err != nil {
			return fmt.Errorf("hook: %w", err)
		}
	}
	if slices.Contains(want, "exec") {
		if err := bench("exec /bin/true (B-080)", runs, dir, nil, bob, "exec", "--project", dir, "--", "/bin/true"); err != nil {
			return err
		}
	}
	if slices.Contains(want, "shim") {
		argv := strings.Fields(shimArgs)
		argv[0] = filepath.Join(dir, ".bob", "bin", argv[0])
		if err := bench("shim "+shimArgs+" (B-083 incl. tool)", runs/5, dir, nil, argv...); err != nil {
			return err
		}
	}
	if slices.Contains(want, "dev") {
		if err := bench("bob dev -- true (B-014)", devRuns, dir, nil, bob, "dev", "--", "true"); err != nil {
			return err
		}
	}
	if slices.Contains(want, "lsp") {
		if err := benchLSP(dir, lspFile, lspNeedle); err != nil {
			return fmt.Errorf("lsp: %w", err)
		}
	}
	fmt.Printf("load after: %s\n", load())
	return nil
}

// benchHook enters the project once and measures the fast path with the resulting state.
func benchHook(bob, dir string, runs int) error {
	clean := withoutBobVars(os.Environ())
	cmd := exec.Command(bob, "env", "--hook=bash")
	cmd.Dir, cmd.Env, cmd.Stderr = dir, clean, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("enter: %w", err)
	}
	state := exports(string(out))
	if state["BOB_PROJECT"] != dir {
		return fmt.Errorf("enter script sets BOB_PROJECT=%q (is the project allowed?)", state["BOB_PROJECT"])
	}
	env := slices.Clone(clean)
	for k, v := range state {
		env = append(env, k+"="+v)
	}
	return bench("hook fast path (B-079)", runs, dir, env, bob, "env", "--hook=bash")
}

// bench runs argv runs times (after one warm-up run) and prints mean, median, p95 and max.
func bench(name string, runs int, dir string, env []string, argv ...string) error {
	one := func() (time.Duration, error) {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = dir, env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		t0 := time.Now()
		out, err := cmd.Output()
		d := time.Since(t0)
		if err != nil {
			return 0, fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, stderr.String())
		}
		if strings.Contains(name, "hook") && len(out) > 0 {
			return 0, fmt.Errorf("hook left the fast path: %q", out)
		}
		return d, nil
	}
	if _, err := one(); err != nil {
		return err
	}
	ds := make([]time.Duration, 0, runs)
	for range max(runs, 1) {
		d, err := one()
		if err != nil {
			return err
		}
		ds = append(ds, d)
	}
	report(name, ds)
	return nil
}

func report(name string, ds []time.Duration) {
	slices.Sort(ds)
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	q := func(p float64) time.Duration { return ds[min(len(ds)-1, int(p*float64(len(ds))))] }
	mean := sum / time.Duration(len(ds))
	fmt.Printf("%-40s n=%-3d min %6.2f  mean %6.2f  median %6.2f  p95 %6.2f  max %6.2f ms  (load %s)\n",
		name, len(ds), ms(ds[0]), ms(mean), ms(q(0.5)), ms(q(0.95)), ms(ds[len(ds)-1]), load())
	if !budgetLines {
		return
	}
	switch {
	case strings.HasPrefix(name, "hook fast path"):
		fmt.Printf("BUDGET B-079 %.3f\nBUDGET B-019 %.3f\n", ms(q(0.95)), ms(q(0.95)))
	case strings.HasPrefix(name, "exec /bin/true"):
		fmt.Printf("BUDGET B-080 %.3f\nBUDGET B-018 %.3f\n", ms(q(0.95)), ms(q(0.5)))
	case strings.HasPrefix(name, "bob dev -- true"):
		fmt.Printf("BUDGET B-014 %.4f\n", q(0.5).Seconds())
	case strings.HasPrefix(name, "warm definition"):
		fmt.Printf("BUDGET B-081 %.3f\n", ms(mean))
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// load is /proc/loadavg's first three fields.
func load() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "?"
	}
	return strings.Join(strings.Fields(string(b))[:3], " ")
}

func withoutBobVars(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, "BOB_") })
}

// exports parses the `export K='v';` lines of a bash enter script.
func exports(script string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(script, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "export ")
		if !ok {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSuffix(rest, ";"), "=")
		if ok {
			m[k] = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'"), `'\''`, "'")
		}
	}
	return m
}

// benchLSP starts .bob/bin/gopls, opens file and measures 20 warm definition requests.
func benchLSP(dir, file, needle string) error {
	cmd := exec.Command(filepath.Join(dir, ".bob", "bin", "gopls"))
	cmd.Dir = dir
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	outp, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	c := &lsp{in: in, out: bufio.NewReader(outp)}
	uri := "file://" + dir
	if _, err := c.call("initialize", map[string]any{"processId": os.Getpid(), "rootUri": uri,
		"capabilities": map[string]any{}, "workspaceFolders": []any{map[string]string{"uri": uri, "name": "w"}}}); err != nil {
		return err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	path := filepath.Join(dir, file)
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fileURI := "file://" + path
	if err := c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": fileURI, "languageId": "go", "version": 1, "text": string(text)}}); err != nil {
		return err
	}
	i := strings.Index(string(text), needle)
	if i < 0 {
		return fmt.Errorf("%s not in %s", needle, file)
	}
	i += 5
	line := strings.Count(string(text[:i]), "\n")
	char := i - (strings.LastIndexByte(string(text[:i]), '\n') + 1)
	params := map[string]any{"textDocument": map[string]string{"uri": fileURI},
		"position": map[string]int{"line": line, "character": char}}
	t0 := time.Now()
	res, err := c.call("textDocument/definition", params)
	if err != nil {
		return err
	}
	fmt.Printf("%-40s first definition %.1f ms: %s\n", "lsp", ms(time.Since(t0)), trim(string(res), 160))
	ds := make([]time.Duration, 0, 20)
	for range 20 {
		t0 := time.Now()
		if _, err := c.call("textDocument/definition", params); err != nil {
			return err
		}
		ds = append(ds, time.Since(t0))
	}
	report("warm definition via shim+proxy (B-081)", ds)
	_, _ = c.call("shutdown", nil)
	_ = c.notify("exit", nil)
	return nil
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// lsp is a minimal LSP client: requests the server sends are answered with null.
type lsp struct {
	in  io.Writer
	out *bufio.Reader
	id  int
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *lsp) send(v map[string]any) error {
	v["jsonrpc"] = "2.0"
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(b), b)
	return err
}

func (c *lsp) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

func (c *lsp) call(method string, params any) (json.RawMessage, error) {
	c.id++
	if err := c.send(map[string]any{"id": c.id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	want := strconv.Itoa(c.id)
	for {
		m, err := c.read()
		if err != nil {
			return nil, err
		}
		switch {
		case m.Method != "" && m.ID != nil: // a server request
			if err := c.send(map[string]any{"id": m.ID, "result": nil}); err != nil {
				return nil, err
			}
		case m.Method == "" && string(m.ID) == want:
			if m.Error != nil {
				return nil, errors.New(method + ": " + m.Error.Message)
			}
			return m.Result, nil
		}
	}
}

func (c *lsp) read() (*message, error) {
	n := -1
	for {
		l, err := c.out.ReadString('\n')
		if err != nil {
			return nil, err
		}
		l = strings.TrimSpace(l)
		if l == "" {
			break
		}
		if v, ok := strings.CutPrefix(l, "Content-Length: "); ok {
			if n, err = strconv.Atoi(v); err != nil {
				return nil, err
			}
		}
	}
	if n < 0 || n > 64<<20 {
		return nil, fmt.Errorf("bad Content-Length %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c.out, b); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
