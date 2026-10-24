package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/eval"
)

// nixVersion is the version of the embedded static Nix (ATTRIBUTION.md, R-NIX-020).
const nixVersion = "2.34.9"

// bobVersion is this bob's version (the framework module version, R-FW-044).
func bobVersion() string { return eval.BobVersion() }

// isTerminal reports whether w is a terminal (TCGETS succeeds on its descriptor).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// lockName is the file that marks a repository root (SPEC §1.2).
const lockName = "bob.lock"

// findRoot returns the repository root of dir: the nearest ancestor (dir included) that holds
// bob.lock. With orGit (bob lock only, SPEC §02 open item 11), a repository without bob.lock
// has its root at the nearest ancestor that holds .git. Not found is BOB-E-0002.
func findRoot(dir string, orGit bool) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, lockName)); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if orGit {
		for d := dir; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				return d, nil
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return "", diag.New(2, "dir", dir)
}

// root resolves the repository root of the command.
func (e *env) root(orGit bool) (string, error) { return findRoot(e.cwd, orGit) }

// patterns returns the label patterns of the command's positional arguments; none means every
// package below the current directory (SPEC §02.1).
func (e *env) patterns(root string, pos []string) []string {
	if len(pos) > 0 {
		out := make([]string, len(pos))
		for i, p := range pos {
			out[i] = resolvePattern(e.relDir(root), p)
		}
		return out
	}
	if rel := e.relDir(root); rel != "" {
		return []string{"//" + rel + "/..."}
	}
	return []string{"//..."}
}

// relDir is the current directory relative to root, "" at the root or outside it.
func (e *env) relDir(root string) string {
	rel, err := filepath.Rel(root, e.cwd)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// resolvePattern makes a command-line label or pattern absolute (SPEC R-FW-015: the relative
// forms ":name", "./...", "." resolve against the current directory rel, A455) and writes the
// short form "//dir" as the label it stands for, "//dir:<last element of dir>" (the pattern
// grammar's "//a/b" = "//a/b:b"). "." is every package of the current directory
// ("//rel:all"); "./<dir>…" is "<dir>…" below it. Everything else is returned unchanged.
func resolvePattern(rel, p string) string {
	abs := func(dir string) string {
		switch {
		case rel == "":
			return dir
		case dir == "":
			return rel
		}
		return rel + "/" + dir
	}
	switch {
	case p == ".":
		return "//" + abs("") + ":all"
	case p == "./...":
		if rel == "" {
			return "//..."
		}
		return "//" + rel + "/..."
	case strings.HasPrefix(p, ":"):
		return "//" + abs("") + p
	case strings.HasPrefix(p, "./"):
		p = "//" + abs(strings.TrimPrefix(p, "./"))
	}
	if dir, ok := strings.CutPrefix(p, "//"); ok && dir != "" && dir != "..." &&
		!strings.ContainsAny(dir, ":@") && !strings.HasSuffix(dir, "/...") {
		return p + ":" + path.Base(dir)
	}
	return p
}

// configDir is $XDG_CONFIG_HOME/bob (default ~/.config/bob).
func configDir(getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bob")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".config", "bob")
}

// maxConfigBytes bounds the config files bob reads (hand-edited input).
const maxConfigBytes = 1 << 20

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxConfigBytes {
		return errors.New("file too large")
	}
	return json.Unmarshal(b, v)
}
