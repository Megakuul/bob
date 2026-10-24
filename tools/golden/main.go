// Command golden reviews and approves golden candidates (HARNESS.md §3.2).
//
//	go run ./tools/golden review             builds out/golden-review/index.html
//	go run ./tools/golden approve <ids|all>  copies candidates into place, rewrites the manifest
//
// An id is the golden's repo-relative path, e.g. tests/goldens/ir/acme.json. After Gate H
// approving is the owner's step; an agent's approval shows up as a protected change (D70).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const metaDir = ".meta"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "golden:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fl := flag.NewFlagSet("golden", flag.ContinueOnError)
	root := fl.String("root", ".", "repository root")
	cands := fl.String("candidates", "", "candidate directory (default <root>/out/golden-candidates)")
	out := fl.String("out", "", "review output directory (default <root>/out/golden-review)")
	noProtect := fl.Bool("no-protect", false, "approve: do not rewrite tests/protected.sha256")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *cands == "" {
		*cands = filepath.Join(*root, "out", "golden-candidates")
	}
	if *out == "" {
		*out = filepath.Join(*root, "out", "golden-review")
	}
	rest := fl.Args()
	if len(rest) == 0 {
		return errors.New("usage: golden [flags] review | approve <ids|all>")
	}
	switch rest[0] {
	case "review":
		entries, err := collect(*root, *cands)
		if err != nil {
			return err
		}
		idx, err := writeReview(*out, entries)
		if err != nil {
			return err
		}
		fmt.Printf("%d changed golden(s); review page: %s\n", len(entries), idx)
		return nil
	case "approve":
		if len(rest) < 2 {
			return errors.New("approve needs <ids|all>")
		}
		n, err := approve(*root, *cands, rest[1:])
		if err != nil {
			return err
		}
		fmt.Printf("approved %d golden(s)\n", n)
		if n == 0 || *noProtect {
			return nil
		}
		cmd := exec.CommandContext(ctx, "go", "run", "./tools/ci", "protect", "-write")
		cmd.Dir, cmd.Stdout, cmd.Stderr = *root, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("rewrite protected manifest: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

// entry is one changed golden.
type entry struct {
	ID     string // repo-relative golden path
	Old    []byte // nil when the golden is new
	New    []byte
	Test   string
	Reqs   []string
	exists bool
}

// collect lists candidates that differ from their approved golden, sorted by id.
func collect(root, cands string) ([]entry, error) {
	var es []entry
	err := filepath.WalkDir(cands, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == cands {
				return fs.SkipAll
			}
			return err
		}
		rel, err := filepath.Rel(cands, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == metaDir {
				return fs.SkipDir
			}
			return nil
		}
		id := filepath.ToSlash(rel)
		e := entry{ID: id}
		if e.New, err = os.ReadFile(p); err != nil {
			return fmt.Errorf("read candidate: %w", err)
		}
		old, err := os.ReadFile(filepath.Join(root, rel))
		switch {
		case err == nil:
			e.Old, e.exists = old, true
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("read golden: %w", err)
		}
		if e.exists && bytes.Equal(e.Old, e.New) {
			return nil // already approved
		}
		if mb, err := os.ReadFile(filepath.Join(cands, metaDir, rel+".json")); err == nil {
			var m struct {
				Test string   `json:"test"`
				Reqs []string `json:"reqs"`
			}
			if err := json.Unmarshal(mb, &m); err != nil {
				return fmt.Errorf("meta for %s: %w", id, err)
			}
			e.Test, e.Reqs = m.Test, m.Reqs
		}
		es = append(es, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect candidates: %w", err)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].ID < es[j].ID })
	return es, nil
}

// approve copies the selected candidates into place and removes them from the candidate dir.
func approve(root, cands string, ids []string) (int, error) {
	es, err := collect(root, cands)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	all := len(ids) == 1 && ids[0] == "all"
	for _, id := range ids {
		want[filepath.ToSlash(filepath.Clean(id))] = true
	}
	known := map[string]bool{}
	n := 0
	for _, e := range es {
		known[e.ID] = true
		if !all && !want[e.ID] {
			continue
		}
		if !strings.HasPrefix(e.ID, "tests/") {
			return n, fmt.Errorf("refusing to approve %s: goldens live under tests/", e.ID)
		}
		dst := filepath.Join(root, filepath.FromSlash(e.ID))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return n, fmt.Errorf("approve %s: %w", e.ID, err)
		}
		if err := os.WriteFile(dst, e.New, 0o644); err != nil {
			return n, fmt.Errorf("approve %s: %w", e.ID, err)
		}
		_ = os.Remove(filepath.Join(cands, filepath.FromSlash(e.ID)))
		_ = os.Remove(filepath.Join(cands, metaDir, filepath.FromSlash(e.ID)+".json"))
		n++
	}
	if !all {
		for id := range want {
			if !known[id] {
				return n, fmt.Errorf("no changed candidate %s", id)
			}
		}
	}
	return n, nil
}
