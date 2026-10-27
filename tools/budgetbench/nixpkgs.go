package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// nixpkgsSurface measures the generated typed nixpkgs surface (ADR 0020 §4, rows B-098…B-103):
//
//	B-098  size of pkg/nixpkgs/attrs_gen.go (MiB; the handle count is a note)
//	B-099  cold `go build ./pkg/nixpkgs` (empty GOCACHE), median of runs
//	B-100  cold compile delta of a bob.go program using a typed handle (pkg/nixpkgs) against the
//	       same program naming the attribute through nixext (the string-API shape), median
//	B-101  gopls first completion in a fresh session (workspace load) on a bob.go importing
//	       pkg/nixpkgs, delta against one that does not, median
//	B-102  gopls completion of `nixpkgs.Op` (first item Openssl), p95 of 20 warm requests
//	B-103  binary size delta of the B-100 programs (MiB)
//
// gopls is the one of the nixpkgs pin in bob's store (or $BOB_BENCH_GOPLS).
func (b *bench) nixpkgsSurface() error {
	gen := filepath.Join(b.root, "pkg", "nixpkgs", "attrs_gen.go")
	src, err := os.ReadFile(gen)
	if err != nil {
		return err
	}
	b.budget("B-098", float64(len(src))/mib, "MiB", "value", nil)
	b.note("attrs_gen.go: %d handles", len(regexp.MustCompile(`(?m)^\t[A-Za-z][A-Za-z0-9_]* = nixext\.New\(`).FindAll(src, -1)))

	mod, err := b.surfaceModule()
	if err != nil {
		return err
	}
	goEnv := func(cache string) []string {
		return append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=mod", "GOCACHE="+cache)
	}
	cold := func(dir string, args ...string) (float64, error) {
		cache := filepath.Join(b.work, "nixpkgs-gocache")
		if err := os.RemoveAll(cache); err != nil {
			return 0, err
		}
		cmd := exec.Command("go", args...)
		cmd.Dir, cmd.Env = dir, goEnv(cache)
		t0 := time.Now()
		if out, err := cmd.CombinedOutput(); err != nil {
			return 0, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out)
		}
		return time.Since(t0).Seconds(), nil
	}
	var build, delta []float64
	for range b.runs {
		s, err := cold(b.root, "build", "./pkg/nixpkgs")
		if err != nil {
			return err
		}
		build = append(build, s)
		typed, err := cold(mod, "build", "-o", os.DevNull, "./typed")
		if err != nil {
			return err
		}
		str, err := cold(mod, "build", "-o", os.DevNull, "./strings")
		if err != nil {
			return err
		}
		delta = append(delta, typed-str)
	}
	b.budget("B-099", median(build), "s", "median", build)
	b.budget("B-100", median(delta), "s", "median", delta)

	sizes := map[string]int64{}
	for _, p := range []string{"typed", "strings"} {
		out := filepath.Join(b.work, "nixpkgs-"+p+".bin")
		cmd := exec.Command("go", "build", "-o", out, "./"+p)
		cmd.Dir, cmd.Env = mod, goEnv(filepath.Join(b.work, "nixpkgs-gocache"))
		if o, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build ./%s: %w\n%s", p, err, o)
		}
		fi, err := os.Stat(out)
		if err != nil {
			return err
		}
		sizes[p] = fi.Size()
	}
	b.budget("B-103", float64(sizes["typed"]-sizes["strings"])/mib, "MiB", "value", nil)

	gopls, err := b.gopls()
	if err != nil {
		return err
	}
	var load []float64
	var warm []float64
	for i := range b.runs {
		with, w, err := lspComplete(gopls, mod, "a/bob.go", "nixpkgs.Op", goEnv(filepath.Join(b.work, "nixpkgs-gocache")))
		if err != nil {
			return err
		}
		without, pw, err := lspComplete(gopls, mod, "plain/bob.go", "bob.Gl", goEnv(filepath.Join(b.work, "nixpkgs-gocache")))
		if err != nil {
			return err
		}
		load = append(load, with-without)
		if i == 0 {
			warm = w
			b.note("reference: warm completion of bob.Gl (no nixpkgs import) p95 %.1f ms under the same load", p95(pw))
		}
	}
	b.budget("B-101", median(load), "s", "median", load)
	b.budget("B-102", p95(warm), "ms", "p95", warm)
	return nil
}

// surfaceModule writes the measurement module: typed/ uses nixpkgs.Openssl, strings/ the same
// attribute through nixext.New (what the removed string API compiled to), a/bob.go and
// plain/bob.go are the completion files.
func (b *bench) surfaceModule() (string, error) {
	dir := filepath.Join(b.work, "nixpkgs-surface")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	sum, err := os.ReadFile(filepath.Join(b.root, "go.sum"))
	if err != nil {
		return "", err
	}
	main := func(imp, expr string) string {
		return "package main\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/" + imp + "\"\n)\n\n" +
			"var X = bob.Define(func(p *bob.P) bob.Tree { return " + expr + " })\n\nfunc main() { bob.Main() }\n"
	}
	files := map[string]string{
		"go.mod":          "module example.com/surface\n\ngo 1.26.0\n\nrequire github.com/megakuul/bob v0.0.0\n\nreplace github.com/megakuul/bob => " + b.root + "\n",
		"go.sum":          string(sum),
		"typed/main.go":   main("nixpkgs", "nixpkgs.Openssl.Lib(p)"),
		"strings/main.go": main("nixpkgs/nixext", `nixext.New("openssl").Lib(p)`),
		"a/bob.go":        "//go:build bob\n\npackage a\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/nixpkgs\"\n)\n\nvar X = bob.Define(func(p *bob.P) bob.Tree { return nixpkgs.Openssl.Lib(p) })\n\nvar _ = nixpkgs.Op\n",
		"plain/bob.go":    "//go:build bob\n\npackage plain\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar X = bob.Glob(\"*.c\")\n\nvar _ = bob.Gl\n",
		"plain/x.c":       "int x;\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// gopls returns the gopls of the nixpkgs pin from bob's store ($BOB_BENCH_GOPLS overrides).
func (b *bench) gopls() (string, error) {
	if g := os.Getenv("BOB_BENCH_GOPLS"); g != "" {
		return g, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	ms, _ := filepath.Glob(filepath.Join(home, ".local", "share", "bob", "nix", "store", "*-gopls-*", "bin", "gopls"))
	slices.Sort(ms)
	if len(ms) == 0 {
		return "", errors.New("no gopls in bob's store; run bob dev once in a Go project, or set BOB_BENCH_GOPLS")
	}
	return ms[len(ms)-1], nil
}

// lspComplete starts gopls in dir (GOFLAGS -tags=bob), opens file and requests completion at
// the end of needle: the first answer's latency (s, includes the workspace load) and 20 warm
// requests (ms). For nixpkgs.Op the answer must offer Openssl (its rank is printed).
func lspComplete(gopls, dir, file, needle string, env []string) (float64, []float64, error) {
	cmd := exec.Command(gopls)
	cmd.Dir = dir
	cmd.Env = append(slices.Clone(env), "GOFLAGS=-mod=mod -tags=bob")
	in, err := cmd.StdinPipe()
	if err != nil {
		return 0, nil, err
	}
	outp, err := cmd.StdoutPipe()
	if err != nil {
		return 0, nil, err
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	c := &lspClient{in: in, out: bufio.NewReader(outp)}
	uri := "file://" + dir
	t0 := time.Now()
	if _, err := c.call("initialize", map[string]any{"processId": os.Getpid(), "rootUri": uri,
		"capabilities": map[string]any{}, "workspaceFolders": []any{map[string]string{"uri": uri, "name": "w"}}}); err != nil {
		return 0, nil, err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return 0, nil, err
	}
	path := filepath.Join(dir, file)
	text, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	fileURI := "file://" + path
	if err := c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": fileURI, "languageId": "go", "version": 1, "text": string(text)}}); err != nil {
		return 0, nil, err
	}
	i := strings.LastIndex(string(text), needle)
	if i < 0 {
		return 0, nil, fmt.Errorf("%s not in %s", needle, file)
	}
	i += len(needle)
	line := strings.Count(string(text[:i]), "\n")
	char := i - (strings.LastIndexByte(string(text[:i]), '\n') + 1)
	params := map[string]any{"textDocument": map[string]string{"uri": fileURI},
		"position": map[string]int{"line": line, "character": char}}
	res, err := c.call("textDocument/completion", params)
	if err != nil {
		return 0, nil, err
	}
	first := time.Since(t0).Seconds()
	if needle == "nixpkgs.Op" {
		var cl struct {
			Items []struct {
				Label string `json:"label"`
			} `json:"items"`
		}
		rank := -1
		if err := json.Unmarshal(res, &cl); err == nil {
			for i, it := range cl.Items {
				if it.Label == "Openssl" {
					rank = i
					break
				}
			}
		}
		if rank < 0 {
			return 0, nil, fmt.Errorf("completion of nixpkgs.Op does not offer Openssl: %.300s", res)
		}
		fmt.Printf("# completion of nixpkgs.Op offers Openssl at rank %d of %d (gopls orders equal prefix matches itself)\n", rank+1, len(cl.Items))
	}
	var warm []float64
	for range 20 {
		t := time.Now()
		if _, err := c.call("textDocument/completion", params); err != nil {
			return 0, nil, err
		}
		warm = append(warm, float64(time.Since(t).Microseconds())/1000)
	}
	_, _ = c.call("shutdown", nil)
	_ = c.notify("exit", nil)
	return first, warm, nil
}

// p95 is the 95th percentile (nearest rank).
func p95(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	k := (95*len(s) + 99) / 100
	return s[max(k-1, 0)]
}

// lspClient is a minimal LSP client (requests of the server are answered with null).
type lspClient struct {
	in  io.Writer
	out *bufio.Reader
	id  int
}

func (c *lspClient) send(v map[string]any) error {
	v["jsonrpc"] = "2.0"
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(b), b)
	return err
}

func (c *lspClient) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

func (c *lspClient) call(method string, params any) (json.RawMessage, error) {
	c.id++
	if err := c.send(map[string]any{"id": c.id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	want := strconv.Itoa(c.id)
	for {
		var n int
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
				n, _ = strconv.Atoi(v)
			}
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.out, buf); err != nil {
			return nil, err
		}
		var m struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal(buf, &m); err != nil {
			return nil, err
		}
		switch {
		case m.Method != "" && m.ID != nil:
			if err := c.send(map[string]any{"id": m.ID, "result": nil}); err != nil {
				return nil, err
			}
		case m.Method == "" && string(m.ID) == want:
			if m.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
			}
			return m.Result, nil
		}
	}
}
