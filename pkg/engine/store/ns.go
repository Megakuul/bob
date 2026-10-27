package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// The namespace mechanism of ADR 0002 Decision 1 (SPEC R-NIX-021): bob re-executes
// /proc/self/exe with CLONE_NEWUSER|CLONE_NEWNS and a single uid/gid mapping (0 → the real
// ids, setgroups denied). The child makes / recursively private, then either binds <home>/nix
// over the host's /nix or, when the host has none, builds a tmpfs root that mirrors the host's
// top-level entries plus nix/, pivots into it and detaches the old root.
//
// The child is this same binary: init (below) recognises the marker argument before main runs,
// so it works for bob, bob-server and test binaries alike. It reports the outcome of the setup on
// fd 3, which is close-on-exec: "k" means the namespace is ready (EOF follows at exec), "!caps …"
// means the first mount was denied (the Ubuntu AppArmor default; exit 125), "!setup …" any other
// setup failure.

const (
	childMarker    = "__bob_namespace"
	exitCapsDenied = 125
	statusFD       = 3
)

// Child modes.
const (
	modeExec  = "exec"  // exec a program inside: <path> <argv0> <args…>
	modeProbe = "probe" // set up the namespace, report and exit 0
	modeSelf  = "self"  // continue into main with the original arguments: <argv0> <args…>
)

// entry is how long the namespace entry of this process took. It is written once, by init in a
// re-executed child (modeSelf), before main runs, and only read afterwards.
var entry time.Duration

func init() {
	if len(os.Args) > 1 && os.Args[1] == childMarker {
		nsChild(os.Args[2:])
	}
}

// Entered reports whether this process is a bob command re-executed by Enter inside bob's
// namespace, and how long entering it took (from the outer process' clone to the end of the
// mount setup; the `namespace` stage event's durationMs).
func Entered() (time.Duration, bool) { return entry, entry > 0 }

// nsChild runs in the re-executed child. It never returns on failure or in modes exec/probe.
func nsChild(a []string) {
	status := os.NewFile(statusFD, "bob-namespace-status")
	fail := func(kind string, code int, err error) {
		if status != nil {
			fmt.Fprintf(status, "!%s %v", kind, err)
		}
		fmt.Fprintf(os.Stderr, "bob: namespace setup: %v\n", err)
		syscall.Exit(code) // the child has run no bob code yet; there is nothing to clean up
	}
	if len(a) >= 3 && a[2] == modeHostView {
		hostViewChild(a) // does not return
	}
	if len(a) < 3 {
		fail("setup", 126, errors.New("bad arguments"))
	}
	home, mode := a[0], a[2]
	start, _ := strconv.ParseInt(a[1], 10, 64)
	if err := setupNamespace(home); err != nil {
		if errors.Is(err, errCapsDenied) {
			fail("caps", exitCapsDenied, err)
		}
		fail("setup", 126, err)
	}
	syscall.CloseOnExec(statusFD)
	switch mode {
	case modeProbe:
		_, _ = io.WriteString(status, "k")
		syscall.Exit(0)
	case modeSelf:
		if len(a) < 4 {
			fail("setup", 126, errors.New("bad arguments"))
		}
		entry = time.Duration(time.Now().UnixNano() - start)
		if entry <= 0 {
			entry = time.Nanosecond
		}
		_, _ = io.WriteString(status, "k")
		status.Close()
		os.Args = append([]string{a[3]}, a[4:]...)
		return
	case modeExec:
		if len(a) < 5 {
			fail("setup", 126, errors.New("bad arguments"))
		}
		path, argv := a[3], a[4:]
		if _, err := os.Stat(path); err != nil {
			fail("setup", 127, err)
		}
		dropAmbient() // a program of the user's session runs as the user (realuser.go)
		_, _ = io.WriteString(status, "k")
		err := syscall.Exec(path, argv, os.Environ())
		fmt.Fprintf(os.Stderr, "bob: exec %s: %v\n", path, err)
		syscall.Exit(127)
	}
	fail("setup", 126, fmt.Errorf("unknown mode %q", mode))
}

var errCapsDenied = errors.New("mounting inside the user namespace is not permitted")

// setupNamespace does the mounts of Decision 1 inside a fresh user + mount namespace.
func setupNamespace(home string) error {
	if err := testMountErrno(); err != nil {
		return fmt.Errorf("%w: %v", errCapsDenied, err)
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return fmt.Errorf("%w: %v", errCapsDenied, err)
		}
		return fmt.Errorf("make / private: %w", err)
	}
	cwd, _ := os.Getwd()
	store := filepath.Join(home, "nix")
	if fi, err := os.Stat("/nix"); err == nil && fi.IsDir() {
		// The dev shell's host store view (A551) needs the host's /nix after it is covered.
		keepHostNix(home)
		// /etc links into the host store (NixOS) must resolve after /nix is covered (A470).
		if err := mirrorEtc("/etc", filepath.Join(home, "nsetc"), "/nix"); err != nil {
			return err
		}
		if err := syscall.Mount(store, "/nix", "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("bind %s on /nix: %w", store, err)
		}
		return nil
	}
	if err := pivotRoot(filepath.Join(home, "nsroot"), store); err != nil {
		return err
	}
	if cwd == "" || os.Chdir(cwd) != nil {
		_ = os.Chdir("/")
	}
	return nil
}

// pivotRoot builds a tmpfs root at newRoot that mirrors the host's top-level entries (recursive
// binds; symlinks recreated) plus nix/ bound to store, pivots into it and detaches the old root.
// chroot is not used: it would prevent the nested user namespaces of the Nix sandbox.
func pivotRoot(newRoot, store string) error {
	if err := syscall.Mount("tmpfs", newRoot, "tmpfs", 0, "mode=755"); err != nil {
		return fmt.Errorf("tmpfs root: %w", err)
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
	nixDir := filepath.Join(newRoot, "nix")
	if err := os.Mkdir(nixDir, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(store, nixDir, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind store: %w", err)
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

// inNamespace reports whether this process already sees <home>/nix as /nix, i.e. runs inside
// bob's namespace for home. It compares device and inode, so no environment variable can fake it.
func inNamespace(home string) bool {
	var a, b syscall.Stat_t
	if syscall.Stat("/nix", &a) != nil || syscall.Stat(filepath.Join(home, "nix"), &b) != nil {
		return false
	}
	if a.Dev != b.Dev || a.Ino != b.Ino {
		return false
	}
	// A dev session's host store view (hostview.go) puts an overlay on /nix/store: a bob started
	// there is not in the namespace of builds and enters its own, in which the host's store is
	// hidden again.
	if syscall.Stat("/nix/store", &a) == nil && syscall.Stat(filepath.Join(home, "nix", "store"), &b) == nil {
		return a.Dev == b.Dev && a.Ino == b.Ino
	}
	return true
}

// prepareNamespace configures cmd to start in a new namespace for home with the child arguments
// rest (mode first) and returns the read end of the status pipe; the caller closes it.
func prepareNamespace(cmd *exec.Cmd, home string, rest ...string) (*os.File, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("store: status pipe: %w", err)
	}
	cmd.Path = "/proc/self/exe"
	cmd.Args = append([]string{"bob", childMarker, home, strconv.FormatInt(time.Now().UnixNano(), 10)}, rest...)
	cmd.ExtraFiles = []*os.File{w}
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS
	attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
	attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
	if os.Getuid() == 0 && os.Getgid() == 0 && nobodyMapped() {
		// Root may map more than itself: nobody too, so the evaluator sandbox inside this
		// namespace can still drop to nobody instead of running as root (A94).
		attr.UidMappings = append(attr.UidMappings, syscall.SysProcIDMap{ContainerID: nobody, HostID: nobody, Size: 1})
		attr.GidMappings = append(attr.GidMappings, syscall.SysProcIDMap{ContainerID: nobody, HostID: nobody, Size: 1})
	}
	attr.GidMappingsEnableSetgroups = false
	if len(rest) > 0 && rest[0] == modeExec && os.Getuid() != 0 && os.Getgid() != 0 {
		// bob exec: the program is the user's own and runs as the user (realuser.go); the
		// namespace child keeps CAP_SYS_ADMIN for the mounts until it executes the program.
		attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
		attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
		attr.AmbientCaps = append(attr.AmbientCaps, capSysAdmin)
	}
	cmd.SysProcAttr = attr
	return r, w, nil
}

// startNamespace starts cmd (prepared by prepareNamespace) and waits until the child reports
// that the namespace is ready. A refusal is returned as BOB-E-7005 with the exact fix
// (R-NIX-022); the child is reaped in every failure case.
func startNamespace(cmd *exec.Cmd, r, w *os.File) error {
	defer r.Close()
	err := testCloneErrno()
	if err == nil {
		err = cmd.Start()
	}
	w.Close()
	if err != nil {
		return cloneRefusal(err)
	}
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	msg := string(b)
	if msg == "k" {
		return nil
	}
	werr := cmd.Wait()
	switch {
	case strings.HasPrefix(msg, "!caps"):
		return appArmorRefusal(strings.TrimSpace(strings.TrimPrefix(msg, "!caps")))
	case strings.HasPrefix(msg, "!setup"):
		reason := strings.TrimSpace(strings.TrimPrefix(msg, "!setup"))
		d := diag.New(7002, "reason", reason)
		d.Raw = []string{reason}
		return d
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) && ee.ExitCode() == exitCapsDenied {
		return appArmorRefusal("")
	}
	d := diag.New(9005, "reason", fmt.Sprintf("the namespace child ended without a status (%v)", werr))
	return d
}

// Enter makes the running bob command run inside bob's namespace: one entry per command
// (R-NIX-021). In a process that Enter re-executed (or one that already sees bob's store at
// /nix), it returns inside = true at once and the caller carries on. Otherwise it re-executes
// /proc/self/exe with the same arguments, environment and standard streams in a new namespace,
// forwards SIGINT, SIGTERM, SIGHUP and SIGQUIT to it, waits, and returns inside = false with the
// child's exit status, which the caller returns from main. A blocked namespace is BOB-E-7005.
func Enter(ctx context.Context, c Config) (inside bool, exit int, err error) {
	if _, ok := Entered(); ok {
		return true, 0, nil
	}
	home, err := HomeDir(c.Home)
	if err != nil {
		return false, 0, err
	}
	if inNamespace(home) {
		return true, 0, nil
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return false, 0, accessError(err)
	}
	for _, d := range []string{"nix", "nsroot"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			return false, 0, accessError(err)
		}
	}
	// The flake variant's Nix lives in the host store, which the namespace hides: copy it first.
	if err := prepareNix(ctx, home); err != nil {
		return false, 0, err
	}
	// Pdeathsig belongs to the thread that forks: keep this goroutine on one thread until the
	// child is gone, so the child dies with the outer bob and never earlier.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := exec.Command("/proc/self/exe")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	argv0 := "bob"
	if len(os.Args) > 0 {
		argv0 = os.Args[0]
	}
	var rest []string
	if len(os.Args) > 1 {
		rest = os.Args[1:]
	}
	r, w, err := prepareNamespace(cmd, home, append([]string{modeSelf, argv0}, rest...)...)
	if err != nil {
		return false, 0, err
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	if err := startNamespace(cmd, r, w); err != nil {
		return false, 0, err
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("store: wait: panic: %v", p)
			}
		}()
		done <- cmd.Wait()
	}()
	ctxDone := ctx.Done()
	for {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case <-ctxDone:
			ctxDone = nil
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case werr := <-done:
			var ee *exec.ExitError
			if errors.As(werr, &ee) {
				if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
					return false, 128 + int(st.Signal()), nil
				}
				return false, ee.ExitCode(), nil
			}
			if werr != nil {
				return false, 0, fmt.Errorf("store: namespace child: %w", werr)
			}
			return false, 0, nil
		}
	}
}

// nobody is the unprivileged id mapped next to root in a namespace host root enters.
const nobody = 65534

// nobodyMapped reports whether uid and gid 65534 exist in the current user namespace (always
// in the initial one; in a container only when its maps include them).
func nobodyMapped() bool {
	for _, f := range []string{"/proc/self/uid_map", "/proc/self/gid_map"} {
		b, err := os.ReadFile(f)
		if err != nil {
			return false
		}
		ok := false
		for line := range strings.SplitSeq(string(b), "\n") {
			x := strings.Fields(line)
			if len(x) != 3 {
				continue
			}
			in, e1 := strconv.ParseUint(x[0], 10, 32)
			n, e2 := strconv.ParseUint(x[2], 10, 33)
			if e1 == nil && e2 == nil && nobody >= in && nobody < in+n {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// hostViewChild is the child of StartHost: a fresh mount namespace of the dev command. It mounts
// the overlay (a failure is only reported: the program then runs without the host store, which
// is what it did before) and executes the program.
func hostViewChild(a []string) {
	status := os.NewFile(statusFD, "bob-hostview-status")
	fail := func(err error) {
		var errno syscall.Errno
		errors.As(err, &errno)
		if status != nil {
			fmt.Fprintf(status, "!%d %v", int(errno), err)
		}
		syscall.Exit(127)
	}
	if len(a) < 5 {
		fail(errors.New("bad arguments"))
	}
	home, path, argv := a[0], a[3], a[4:]
	if err := mountHostView(home); err != nil {
		fmt.Fprintf(os.Stderr, "bob: the host Nix store is not available here: %v\n", err)
	}
	dropAmbient() // the mounts are done: the program runs as the user (realuser.go)
	syscall.CloseOnExec(statusFD)
	if !strings.Contains(path, "/") {
		p, err := exec.LookPath(path)
		if err != nil {
			fail(err)
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		fail(err)
	}
	err := syscall.Exec(path, argv, os.Environ())
	fail(fmt.Errorf("exec %s: %w", path, err))
}
