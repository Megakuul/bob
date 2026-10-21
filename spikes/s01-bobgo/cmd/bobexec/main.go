// Command bobexec is spike S1's stand-in for `bob exec <tool>`: a copy of spike S0b's bobns
// (spikes/rootless/cmd/bobns) plus -env FILE, the project environment that `bob dev` would set
// (PATH, GOROOT, GOFLAGS, ...), applied inside the namespace before the tool is looked up.
//
// Original comment: Command bobns is a throwaway spike (S0b): it shows how a single rootless `bob` binary can
// provide /nix/store through an unprivileged user + mount namespace and run a static Nix in it.
//
//	bobns [-state DIR] [-force-pivot] [-nix PATH] -- <command> [args...]
//
// Inside the namespace the user appears as uid 0 (mapped to the real uid only), /nix is the
// user-owned DIR/nix, and NIX_CONF_DIR points at DIR/etc (bob-owned config, nothing in /etc).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const childMarker = "__bobns_child"

func main() {
	if len(os.Args) > 1 && os.Args[1] == childMarker {
		if err := child(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bobns(child):", err)
			os.Exit(1)
		}
		return
	}
	if err := parent(); err != nil {
		fmt.Fprintln(os.Stderr, "bobns:", err)
		os.Exit(1)
	}
}

func parent() error {
	home, _ := os.UserHomeDir()
	state := flag.String("state", filepath.Join(home, ".local/share/bob"), "bob state dir")
	forcePivot := flag.Bool("force-pivot", false, "always build a new root (as if /nix did not exist)")
	timing := flag.Bool("timing", false, "print namespace setup time to stderr")
	envFile := flag.String("env", "", "KEY=VALUE file applied inside the namespace (the bob dev environment)")
	hideNix := flag.Bool("hide-nix", false, "test helper: build a root WITHOUT /nix (simulates a host that has no /nix) and run the command there")
	flag.Parse()
	if flag.NArg() == 0 {
		return errors.New("usage: bobns [flags] -- command [args]")
	}
	if os.Getenv("BOBNS_SKIP_PRECHECK") == "" {
		if err := checkUserns(); err != nil {
			return fmt.Errorf("%w\n\n%s", err, fixText(err))
		}
	}
	for _, d := range []string{"nix/store", "nix/var/nix", "etc", "root"} {
		if err := os.MkdirAll(filepath.Join(*state, d), 0o755); err != nil {
			return err
		}
	}
	cwd, _ := os.Getwd()
	mode := fmt.Sprint(*forcePivot)
	if *hideNix {
		mode = "hide"
	}
	os.Setenv("BOBEXEC_ENV", *envFile)
	args := append([]string{childMarker, *state, mode, cwd, fmt.Sprint(*timing), fmt.Sprint(time.Now().UnixNano())}, flag.Args()...)
	cmd := exec.Command("/proc/self/exe", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Pdeathsig:                  syscall.SIGKILL,
	}
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == exitCapsDenied {
			return fmt.Errorf("%w\n\n%s", errCapsDenied, fixText(errCapsDenied))
		}
		os.Exit(ee.ExitCode())
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("cannot create a user namespace (%v)\n\n%s", err, fixText(err))
	}
	return err
}

// checkUserns detects the common reasons unprivileged user namespaces are unavailable so bob can
// print a precise admin fix instead of a raw EPERM (R-NIX-022).
func checkUserns() error {
	if b, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil && strings.TrimSpace(string(b)) == "1" {
		return errors.New("unprivileged user namespaces are restricted by AppArmor (kernel.apparmor_restrict_unprivileged_userns=1)")
	}
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil && strings.TrimSpace(string(b)) == "0" {
		return errors.New("unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone=0)")
	}
	if b, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil && strings.TrimSpace(string(b)) == "0" {
		return errors.New("user namespaces are disabled (user.max_user_namespaces=0)")
	}
	return nil
}

const exitCapsDenied = 125

var errCapsDenied = errors.New("a user namespace was created but mounting inside it is denied (AppArmor default userns profile, e.g. Ubuntu 24.04+)")

// fixText returns the one-time admin fix for a detected restriction (R-NIX-022). bob would print
// its real executable path in the AppArmor profile.
func fixText(err error) string {
	exe, _ := os.Executable()
	msg := err.Error()
	switch {
	case strings.Contains(msg, "AppArmor") || errors.Is(err, errCapsDenied):
		return fmt.Sprintf(`bob needs unprivileged user namespaces. Ask an administrator to allow them for bob only:

  sudo tee /etc/apparmor.d/bob >/dev/null <<'PROFILE'
  abi <abi/4.0>,
  include <tunables/global>
  profile bob %s flags=(unconfined) {
    userns,
    include if exists <local/bob>
  }
  PROFILE
  sudo apparmor_parser -r /etc/apparmor.d/bob

(or, system-wide: echo 'kernel.apparmor_restrict_unprivileged_userns = 0' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system)`, exe)
	case strings.Contains(msg, "unprivileged_userns_clone"):
		return `Ask an administrator to run once:
  echo 'kernel.unprivileged_userns_clone = 1' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system`
	case strings.Contains(msg, "max_user_namespaces") || errors.Is(err, syscall.ENOSPC):
		return `Ask an administrator to run once:
  echo 'user.max_user_namespaces = 15000' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system
(NixOS: set security.allowUserNamespaces = true;)`
	default:
		return "Unprivileged user namespaces appear to be disabled on this system (kernel config or security policy)."
	}
}

func child(a []string) error {
	state, forcePivot, hide, cwd, timing := a[0], a[1] == "true", a[1] == "hide", a[2], a[3] == "true"
	var start int64
	fmt.Sscan(a[4], &start)
	argv := a[5:]

	// Our mounts must never propagate back to the host.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			os.Exit(exitCapsDenied)
		}
		return fmt.Errorf("make / private: %w", err)
	}
	storeDir := filepath.Join(state, "nix")
	if hide {
		if err := pivotRoot(filepath.Join(state, "root"), ""); err != nil {
			return fmt.Errorf("hide-nix root: %w", err)
		}
		_ = os.Chdir(cwd)
		path, err := exec.LookPath(argv[0])
		if err != nil {
			return err
		}
		return syscall.Exec(path, argv, os.Environ())
	}
	fi, err := os.Stat("/nix")
	if err == nil && fi.IsDir() && !forcePivot {
		if err := syscall.Mount(storeDir, "/nix", "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("bind %s on /nix: %w", storeDir, err)
		}
	} else if err := pivotRoot(filepath.Join(state, "root"), storeDir); err != nil {
		return fmt.Errorf("new root: %w", err)
	}
	if err := os.Chdir(cwd); err != nil {
		_ = os.Chdir("/")
	}
	os.Setenv("NIX_CONF_DIR", filepath.Join(state, "etc"))
	os.Setenv("NIX_USER_CONF_FILES", filepath.Join(state, "etc", "nix.user.conf"))
	// Nix also keeps state under XDG dirs (e.g. the narinfo disk cache in ~/.cache/nix); keep it
	// inside bob's state so separate bob stores and the user's own Nix never share it.
	os.Setenv("XDG_CACHE_HOME", filepath.Join(state, "cache"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(state, "xdg-state"))
	os.Setenv("XDG_DATA_HOME", filepath.Join(state, "xdg-data"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(state, "xdg-config"))
	if f := os.Getenv("BOBEXEC_ENV"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && !strings.HasPrefix(k, "#") {
				os.Setenv(k, os.ExpandEnv(v))
			}
		}
	}
	os.Unsetenv("BOBEXEC_ENV")
	if timing {
		fmt.Fprintf(os.Stderr, "bobns: namespace ready after %.2f ms\n", float64(time.Now().UnixNano()-start)/1e6)
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

// pivotRoot builds a root that mirrors the host's top-level entries (recursive binds, symlinks
// recreated) plus /nix, then pivots into it. Used when the host has no /nix directory to mount
// over (we cannot create one in the real / without privileges).
func pivotRoot(newRoot, storeDir string) error {
	if err := syscall.Mount("tmpfs", newRoot, "tmpfs", 0, "mode=755"); err != nil {
		return fmt.Errorf("tmpfs: %w", err)
	}
	entries, err := os.ReadDir("/")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "nix" {
			continue
		}
		src, dst := "/"+name, filepath.Join(newRoot, name)
		fi, err := os.Lstat(src)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(t, dst); err != nil {
				return err
			}
		case fi.IsDir():
			if err := os.Mkdir(dst, 0o755); err != nil {
				return err
			}
			if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
				return fmt.Errorf("bind %s: %w", src, err)
			}
		default:
			f, err := os.Create(dst)
			if err != nil {
				return err
			}
			f.Close()
			if err := syscall.Mount(src, dst, "", syscall.MS_BIND, ""); err != nil {
				return fmt.Errorf("bind %s: %w", src, err)
			}
		}
	}
	if storeDir != "" {
		nixDir := filepath.Join(newRoot, "nix")
		if err := os.Mkdir(nixDir, 0o755); err != nil {
			return err
		}
		if err := syscall.Mount(storeDir, nixDir, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("bind store: %w", err)
		}
	}
	old := filepath.Join(newRoot, ".oldroot")
	if err := os.Mkdir(old, 0o700); err != nil {
		return err
	}
	if err := syscall.PivotRoot(newRoot, old); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return err
	}
	if err := syscall.Unmount("/.oldroot", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	return os.Remove("/.oldroot")
}
