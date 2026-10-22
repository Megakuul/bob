package devenv

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// The trust list of the shell hook (SPEC R-DEV-009): $XDG_STATE_HOME/bob/allow.json,
// {"v":1,"roots":["/abs/path",…]}, mode 0600. A root is trusted by path.

// maxAllowFile bounds allow.json (hand-editable input).
const maxAllowFile = 4 << 20

type allowFile struct {
	V     int      `json:"v"`
	Roots []string `json:"roots"`
}

// AllowPath is the trust list's path for getenv's XDG_STATE_HOME/HOME.
func AllowPath(getenv func(string) string) string {
	if x := getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "bob", "allow.json")
	}
	h := getenv("HOME")
	if h == "" {
		h, _ = os.UserHomeDir()
	}
	return filepath.Join(h, ".local", "state", "bob", "allow.json")
}

// Allowed returns the allowed roots. A missing file is an empty list; a damaged one too (the
// hook then activates nothing, which is the safe side).
func Allowed(p string) []string {
	b, err := readCapped(p, maxAllowFile)
	if err != nil {
		return nil
	}
	var f allowFile
	if json.Unmarshal(b, &f) != nil || f.V != 1 {
		return nil
	}
	return f.Roots
}

// IsAllowed reports whether root is in the trust list at p.
func IsAllowed(p, root string) bool { return contains(Allowed(p), root) }

// SetAllowed adds (allow) or removes root from the trust list at p.
func SetAllowed(p, root string, allow bool) error {
	b, err := readCapped(p, maxAllowFile)
	var f allowFile
	switch {
	case err == nil:
		if json.Unmarshal(b, &f) != nil || f.V != 1 {
			f = allowFile{} // a damaged list is rewritten from scratch
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return err
	}
	set := map[string]bool{}
	for _, r := range f.Roots {
		if filepath.IsAbs(r) {
			set[r] = true
		}
	}
	if allow {
		set[root] = true
	} else {
		delete(set, root)
	}
	roots := make([]string, 0, len(set))
	for r := range set {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	out, err := json.Marshal(allowFile{V: 1, Roots: roots})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Always rewrite: the hook's fast path notices the trust list changed by its stat.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".allow.json.tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
