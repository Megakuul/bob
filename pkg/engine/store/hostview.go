package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The host store view (A551). bob's namespace covers the host's /nix with bob's own store
// (ADR 0002 Decision 1), so inside it every program that lives in the host's store is gone:
// on NixOS, home-manager and nix-profile systems the user's $SHELL (/run/current-system/sw/bin/fish),
// /bin/sh and /usr/bin/env are symlinks into it. `bob dev` and its hooks and servers run the
// user's own programs, so they get a second mount namespace in which /nix/store is a read-only
// overlay: bob's store first, the host's store below it. Everything bob put in the dev shell
// still comes from bob's store (the same hashes are identical anyway); host programs, their
// absolute /nix/store ELF interpreters and the store links of /etc resolve.
//
// Builds never see it: the overlay exists only in the child namespace of StartHost, the build
// sandbox and Nix run in the namespace of the bob command, and a bob started from the dev shell
// enters its own clean namespace (inNamespace does not accept the overlay).
//
// The host's /nix is kept reachable by setupNamespace as a bind mount at <home>/hostnix (the
// bind is of the host's directory, not a copy; nothing writes to it).

const modeHostView = "hostview" // child mode: overlay the host store, then exec <path> <argv0> <args…>

// hostNixDir is where the namespace keeps the host's /nix.
func hostNixDir(home string) string { return filepath.Join(home, "hostnix") }

// keepHostNix binds the host's /nix at <home>/hostnix, unless it is already there (hostnix/store
// exists; an unmounted hostnix is an empty directory) (a bob
// started inside a host-view shell enters a fresh namespace in which the mount is inherited).
// It is best effort: without it the host store view is just not available.
func keepHostNix(home string) {
	dir := hostNixDir(home)
	if home == "" || strings.HasPrefix(home+"/", "/nix/") || isDir(filepath.Join(dir, "store")) {
		return
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	_ = syscall.Mount("/nix", dir, "", syscall.MS_BIND|syscall.MS_REC, "")
}

// HostViewAvailable reports whether the current namespace kept the host's store for home.
func HostViewAvailable(home string) bool {
	return isDir(filepath.Join(hostNixDir(home), "store"))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// StartHost starts cmd like cmd.Start, in a namespace in which the host's Nix store is visible
// below bob's (see above). It is for the user's own programs: the dev shell, `bob dev -- cmd`,
// dev hooks and dev servers. cmd.Path, Args, Env, Dir and the streams are the caller's. Outside
// bob's namespace, on a host without a Nix store or when the view cannot be made it starts cmd
// as it is, so a missing program is reported by the same error as before. A program that cannot
// be executed in the view is returned as an error without a started process.
func StartHost(home string, cmd *exec.Cmd) error {
	h, err := HomeDir(home)
	if err != nil {
		return err
	}
	_, entered := Entered()
	if (!entered && !inNamespace(h)) || cmd.Err != nil {
		return cmd.Start()
	}
	// The program runs as the user, not as the namespace's root (realuser.go).
	uid, gid, real := realIDs()
	if !HostViewAvailable(h) || len(cmd.ExtraFiles) > 0 {
		if real {
			asUser(cmd, uid, gid, false)
		}
		return cmd.Start()
	}
	path, args := cmd.Path, cmd.Args
	if len(args) == 0 {
		args = []string{path}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("store: status pipe: %w", err)
	}
	defer r.Close()
	cmd.Path = "/proc/self/exe"
	cmd.Args = append([]string{"bob", childMarker, h, "0", modeHostView, path}, args...)
	cmd.ExtraFiles = []*os.File{w}
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.Cloneflags |= syscall.CLONE_NEWNS
	cmd.SysProcAttr = attr
	if real {
		asUser(cmd, uid, gid, true)
	}
	err = cmd.Start()
	w.Close()
	cmd.Path, cmd.Args, cmd.ExtraFiles = path, args, nil
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	if msg := string(b); strings.HasPrefix(msg, "!") {
		_ = cmd.Wait()
		num, text, _ := strings.Cut(strings.TrimPrefix(msg, "!"), " ")
		if n, err := strconv.Atoi(num); err == nil && n != 0 {
			return fmt.Errorf("%s: %w", text, syscall.Errno(n))
		}
		return errors.New(text)
	}
	return nil
}

// mountHostView overlays /nix/store (bob's store first, the host's below) and shows the host's
// nix profiles.
func mountHostView(home string) error {
	if strings.ContainsAny(home, ":,\\") {
		return fmt.Errorf("the state directory %q contains one of : , \\", home)
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make / private: %w", err)
	}
	lower := "lowerdir=" + filepath.Join(home, "nix", "store") + ":" + filepath.Join(hostNixDir(home), "store")
	if err := syscall.Mount("overlay", "/nix/store", "overlay", syscall.MS_RDONLY, lower+",userxattr"); err != nil {
		if err2 := syscall.Mount("overlay", "/nix/store", "overlay", syscall.MS_RDONLY, lower); err2 != nil {
			return fmt.Errorf("overlay on /nix/store: %w", err)
		}
	}
	// nix profiles (~/.nix-profile, /nix/var/nix/profiles/per-user/…) are links through
	// /nix/var/nix/profiles into the store; the rest of bob's /nix/var (its database) stays.
	if src := filepath.Join(hostNixDir(home), "var", "nix", "profiles"); isDir(src) {
		dst := "/nix/var/nix/profiles"
		if os.MkdirAll(dst, 0o755) == nil {
			_ = syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, "")
		}
	}
	return nil
}
