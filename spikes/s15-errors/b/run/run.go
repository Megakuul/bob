// Package run puts backend B's derivations into the Nix store, builds them and translates
// Nix's structured log into bob errors.
package run

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"bobspike/s15/b/drv"
)

// ImportSources adds local directories to the store in one nix-store call; returns dir -> store path.
func ImportSources(dirs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(dirs) == 0 {
		return out, nil
	}
	// nix-store --add names each path after its basename; make basenames unique via symlink staging.
	stage, err := os.MkdirTemp("", "bob-src")
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
		cmd := command("nix-store", append([]string{"--add"}, args[i:j]...)...)
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

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
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
		if _, err := os.Stat(d.DrvPath); err != nil || mode == "add" || AlwaysImport {
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
			cmd := command("nix", "derivation", "add")
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
	cmd := command("nix-store", "--import")
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

// NixDir is the directory holding nix and nix-store (S15: switch between Nix versions).
var NixDir = ""

// AlwaysImport imports every drv even if it exists in /nix/store (S15: separate test stores).
var AlwaysImport bool

// ExtraEnv is appended to every Nix child's environment (e.g. NIX_REMOTE for a test store).
var ExtraEnv []string

// Bin returns the path of a Nix executable.
func Bin(name string) string {
	if NixDir == "" {
		return name
	}
	return filepath.Join(NixDir, name)
}

func command(name string, args ...string) *exec.Cmd {
	c := exec.Command(Bin(name), args...)
	c.Env = append(os.Environ(), ExtraEnv...)
	return c
}
