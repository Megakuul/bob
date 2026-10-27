package cli

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/store"
)

// startDev starts a program of the user's dev session (shell, command after --, hooks, dev
// servers). Inside bob's namespace the host's Nix store is mounted below bob's store for it, so
// a $SHELL that is a link into the host store (NixOS, home-manager, nix-profile) runs (A551).
func startDev(c *exec.Cmd) error {
	return store.StartHost("", c)
}

// devStartError is BOB-E-7011 for a program of the dev session that could not be started. A
// program that does not exist but whose path is a link into /nix/store gets its own cause: the
// host store is not visible (no overlay available), which is not a problem of the program.
func devStartError(program string, err error) *diag.Error {
	reason := err.Error()
	hint := "check that " + program + " exists and is executable"
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT):
		if t, ok := hostStoreLink(program); ok {
			reason = "it links to " + t + " in the host's Nix store, which bob's namespace hides (the overlay that shows it could not be mounted)"
			hint = "run it from the dev shell's tools, or set $SHELL to a program of the dev shell or outside /nix/store; bob doctor checks the namespace"
		} else {
			reason = "it does not exist"
			hint = "check the name and PATH; inside bob dev the PATH is the dev shell's followed by yours"
		}
	case errors.Is(err, fs.ErrPermission):
		reason = "it is not executable by you"
	}
	d := diag.New(7011, "program", program, "reason", reason, "hint", hint)
	d.Raw = []string{err.Error()}
	return d
}

// hostStoreLink resolves the symlink chain of p (found on PATH if it has no slash) without
// requiring the target to exist, and returns the first /nix/store path in it.
func hostStoreLink(p string) (string, bool) {
	if !strings.Contains(p, "/") {
		for dir := range strings.SplitSeq(os.Getenv("PATH"), ":") {
			if dir == "" {
				continue
			}
			if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
				p = filepath.Join(dir, p)
				break
			}
		}
	}
	for i := 0; i < 40; i++ {
		if strings.HasPrefix(p, "/nix/store/") {
			return p, true
		}
		t, err := os.Readlink(p)
		if err != nil {
			return "", false
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		p = t
	}
	return "", false
}
