// Package run puts backend B's derivations into the Nix store, builds them and translates
// Nix's structured log into bob errors.
package run

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"bobspike/s05/drv"
)

// ImportSources adds local directories to the store in one nix-store call; returns dir -> store path.
func ImportSources(dirs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(dirs) == 0 {
		return out, nil
	}
	// nix-store --add names each path after its basename; make basenames unique via symlink staging.
	stage, err := os.MkdirTemp(os.Getenv("BOB_TMP"), "bob-src")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	var args []string
	for i, d := range dirs {
		base := filepath.Base(d)
		ln := filepath.Join(stage, fmt.Sprintf("%d", i), base)
		if err := os.MkdirAll(filepath.Dir(ln), 0o755); err != nil {
			return nil, err
		}
		if err := copyTree(d, ln); err != nil {
			return nil, err
		}
		args = append(args, ln)
	}
	const chunk = 2000
	var paths []string
	for i := 0; i < len(args); i += chunk {
		j := min(i+chunk, len(args))
		cmd := exec.Command("nix-store", append([]string{"--add"}, args[i:j]...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("import sources: %v: %s", err, stderr.String())
		}
		paths = append(paths, strings.Fields(string(b))...)
	}
	for i, d := range dirs {
		out[d] = paths[i]
	}
	return out, nil
}

var skipGenerated = map[string]bool{"Cargo.toml": true, "Cargo.lock": true, "target": true, "rust-project.json": true, ".bob": true}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if skipGenerated[fi.Name()] && rel != "." { // generated tooling files are not build inputs
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		t := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if os.Link(p, t) == nil {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, fi.Mode().Perm())
	})
}

// AddDerivations writes derivations to the store. mode "import" streams them through
// `nix-store --import` in one process; mode "add" uses `nix derivation add` per drv and
// checks that Nix computes the same path (consistency proof).
func AddDerivations(ds []*drv.Derivation, mode string) (added int, err error) {
	var todo []*drv.Derivation
	for _, d := range ds {
		if _, err := os.Stat(d.DrvPath); err != nil || mode == "add" {
			todo = append(todo, d) // "add" always asks Nix, as a consistency proof
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}
	if mode == "add" {
		for _, d := range todo {
			j, err := d.JSON()
			if err != nil {
				return added, err
			}
			cmd := exec.Command("nix", "derivation", "add")
			cmd.Stdin = bytes.NewReader(j)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			b, err := cmd.Output()
			if err != nil {
				return added, fmt.Errorf("nix derivation add %s: %v: %s", d.Name, err, stderr.String())
			}
			if got := strings.TrimSpace(string(b)); got != d.DrvPath {
				return added, fmt.Errorf("drv path mismatch for %s: bob %s, nix %s", d.Name, d.DrvPath, got)
			}
			added++
		}
		return added, nil
	}
	var buf bytes.Buffer
	for _, d := range todo {
		writeExport(&buf, d)
	}
	putU64(&buf, 0)
	cmd := exec.Command("nix-store", "--import")
	cmd.Stdin = &buf
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("nix-store --import: %v: %s", err, stderr.String())
	}
	return len(todo), nil
}

func putU64(w *bytes.Buffer, n uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	w.Write(b[:])
}

func putStr(w *bytes.Buffer, s string) {
	putU64(w, uint64(len(s)))
	w.WriteString(s)
	if pad := (8 - len(s)%8) % 8; pad > 0 {
		w.Write(make([]byte, pad))
	}
}

// writeExport writes one path in `nix-store --export` format: a NAR of the .drv text plus metadata.
func writeExport(w *bytes.Buffer, d *drv.Derivation) {
	putU64(w, 1)
	for _, s := range []string{"nix-archive-1", "(", "type", "regular", "contents", d.ATerm(nil), ")"} {
		putStr(w, s)
	}
	putU64(w, 0x4558494e) // "NIXE"
	putStr(w, d.DrvPath)
	refs := append([]string{}, d.InputSrcs...)
	for p := range d.InputDrvs {
		refs = append(refs, p)
	}
	sort.Strings(refs)
	putU64(w, uint64(len(refs)))
	for _, r := range refs {
		putStr(w, r)
	}
	putStr(w, "") // deriver
	putU64(w, 0)  // no signature
}

// Failure is a translated build failure.
type Failure struct {
	Drv  string
	Kind string // "build" | "hash"
	Log  []string
	Msg  string
}

type Result struct {
	OK       bool
	Failures []Failure
	Raw      string // raw stderr for -v
	Built    []string
}

var drvRe = regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^'"\s]+\.drv`)

// Build realises the given derivations and parses Nix's internal-json log.
func Build(drvPaths []string, extra ...string) (*Result, error) {
	args := []string{"build", "--no-link", "--print-out-paths", "--log-format", "internal-json", "-L", "--keep-going"}
	args = append(args, extra...)
	for _, p := range drvPaths {
		args = append(args, p+"^out")
	}
	cmd := exec.Command("nix", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	res := &Result{}
	var raw strings.Builder
	logs := map[float64][]string{}
	act := map[float64]string{}
	var msgs []string
	var mu sync.Mutex
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		raw.WriteString(line + "\n")
		js, ok := strings.CutPrefix(line, "@nix ")
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(js), &m) != nil {
			continue
		}
		mu.Lock()
		id, _ := m["id"].(float64)
		switch m["action"] {
		case "start":
			if t, _ := m["type"].(float64); t == 105 {
				if f, ok := m["fields"].([]any); ok && len(f) > 0 {
					act[id], _ = f[0].(string)
				}
			}
		case "result":
			if t, _ := m["type"].(float64); t == 101 {
				if f, ok := m["fields"].([]any); ok && len(f) > 0 {
					s, _ := f[0].(string)
					logs[id] = append(logs[id], s)
				}
			}
		case "msg":
			if lvl, _ := m["level"].(float64); lvl == 0 {
				s, _ := m["msg"].(string)
				msgs = append(msgs, s)
			}
		}
		mu.Unlock()
	}
	werr := cmd.Wait()
	res.Raw = raw.String()
	res.Built = strings.Fields(stdout.String())
	if werr == nil {
		res.OK = true
		return res, nil
	}
	byDrv := map[string][]string{}
	for id, d := range act {
		byDrv[d] = logs[id]
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		clean := stripANSI(m)
		kind := "build"
		if strings.Contains(clean, "hash mismatch in fixed-output derivation") {
			kind = "hash"
		}
		for _, d := range drvRe.FindAllString(clean, -1) {
			if seen[d] || strings.Contains(clean, "dependency failed") || strings.Contains(clean, "dependencies failed") || strings.Contains(clean, "dependencies couldn't be built") {
				continue
			}
			seen[d] = true
			res.Failures = append(res.Failures, Failure{Drv: d, Kind: kind, Log: byDrv[d], Msg: clean})
			break
		}
	}
	return res, nil
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[mK]`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// StripANSI is exported for log excerpts.
func StripANSI(s string) string { return stripANSI(s) }
