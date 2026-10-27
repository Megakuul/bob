package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// On NixOS (and any host whose /etc links into its Nix store) the files Nix, its build sandbox
// and bob's own fetcher read are symlinks into the host's /nix/store: /etc/resolv.conf,
// /etc/hosts, /etc/services and /etc/ssl/certs/ca-certificates.crt all point through
// /etc/static into it. bob's namespace hides the host's /nix behind bob's store (ADR 0002
// Decision 1), so inside it these links dangle: Nix takes the dangling CA path (it checks with
// lstat), every download from cache.nixos.org fails with "Problem with the SSL CA cert", Nix
// falls back to building the toolchain from source, and the build sandbox then fails on the
// dangling /etc/hosts ("getting attributes of path …: No such file or directory"). Found by Q4
// on an emulated NixOS layout; the owner's first `bob lock` failed exactly so (A470).
//
// mirrorEtc gives the namespace an /etc in which those links resolve: before the host's /nix is
// covered, it builds a tmpfs copy of /etc's top level in which every entry whose symlink chain
// passes through the host store is replaced by a bind mount of its final target (or by a
// symlink to the final target when that lies outside the store), every other entry is a bind
// mount of the original (symlinks recreated verbatim), and the directories on the way to the CA
// bundles are mirrored the same way one level deeper. The copy is then bound over /etc. Hosts
// whose /etc does not link into the store are left untouched (no mounts at all).

// etcProbe are the /etc paths whose resolution decides whether the mirror is needed: the
// resolver and sandbox files (Nix binds resolv.conf, hosts and services into fixed-output
// builds) and the CA bundles of Nix (its first default) and Go (crypto/x509's list).
var etcProbe = []string{
	"resolv.conf", "hosts", "services", "protocols", "nsswitch.conf", "passwd", "group",
	"localtime", "ssl/certs/ca-certificates.crt", "ssl/certs/ca-bundle.crt", "ssl/cert.pem",
	"pki/tls/certs/ca-bundle.crt",
}

// etcRecurse are the directories below /etc that are mirrored entry by entry instead of bound
// whole: the parents of etcProbe's nested paths.
var etcRecurse = map[string]bool{"ssl": true, "ssl/certs": true, "pki": true, "pki/tls": true, "pki/tls/certs": true}

// mirrorEtc builds the mirror of etc in a tmpfs at staging and binds it over etc when one of
// etcProbe's paths links through hidden (the host store directory, "/nix"). It must run in a
// private mount namespace before hidden is covered.
func mirrorEtc(etc, staging, hidden string) error {
	if fi, err := os.Lstat(etc); err != nil || !fi.IsDir() {
		return nil
	}
	need := false
	for _, p := range etcProbe {
		if linksThrough(filepath.Join(etc, filepath.FromSlash(p)), hidden) {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("mirror /etc: %w", err)
	}
	if err := syscall.Mount("tmpfs", staging, "tmpfs", 0, "mode=755"); err != nil {
		return fmt.Errorf("mirror /etc: tmpfs: %w", err)
	}
	mirrorDir(etc, staging, "", hidden)
	if err := syscall.Mount(staging, etc, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("mirror /etc: bind: %w", err)
	}
	// The mirror stays reachable at etc; the staging mount point is not needed any more.
	_ = syscall.Unmount(staging, syscall.MNT_DETACH)
	return nil
}

// mirrorDir fills dst with the entries of src (rel is src's path below /etc, "" for /etc). An
// entry that cannot be mirrored is left out: a missing exotic /etc file must not stop every
// bob command.
func mirrorDir(src, dst, rel, hidden string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		_ = mirrorEntry(filepath.Join(src, name), filepath.Join(dst, name), relJoin(rel, name), hidden)
	}
}

func relJoin(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// mirrorEntry mirrors the single entry p at d.
func mirrorEntry(p, d, rel, hidden string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if !linksThrough(p, hidden) {
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, d)
		}
		final, err := filepath.EvalSymlinks(p)
		if err != nil {
			// Dangling on the host as well: keep it as it is.
			t, rerr := os.Readlink(p)
			if rerr != nil {
				return rerr
			}
			return os.Symlink(t, d)
		}
		if !under(final, hidden) {
			return os.Symlink(final, d)
		}
		if fi, err = os.Stat(final); err != nil {
			return err
		}
		p = final
	}
	switch {
	case fi.IsDir() && etcRecurse[rel]:
		if err := os.Mkdir(d, 0o755); err != nil {
			return err
		}
		mirrorDir(p, d, rel, hidden)
		return nil
	case fi.IsDir():
		if err := os.Mkdir(d, 0o755); err != nil {
			return err
		}
		return syscall.Mount(p, d, "", syscall.MS_BIND|syscall.MS_REC, "")
	}
	f, err := os.OpenFile(d, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	if err := syscall.Mount(p, d, "", syscall.MS_BIND, ""); err != nil {
		_ = os.Remove(d)
		return err
	}
	return nil
}

// linksThrough reports whether p is a symlink whose chain of links passes through hidden.
func linksThrough(p, hidden string) bool {
	cur := p
	for range 40 {
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return false
		}
		t, err := os.Readlink(cur)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(cur), t)
		}
		if under(t, hidden) {
			return true
		}
		if dir, err := filepath.EvalSymlinks(filepath.Dir(t)); err == nil {
			t = filepath.Join(dir, filepath.Base(t))
		}
		if under(t, hidden) {
			return true
		}
		cur = t
	}
	return false
}

// under reports whether p is dir or below it.
func under(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}
