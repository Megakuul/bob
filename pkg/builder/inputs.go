package builder

import (
	"bufio"
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
)

// Read-only inputs (SPEC R-FLEET-008 / R-SEC-000, decision A370). Nix bind-mounts every input
// store path into its sandbox without MS_RDONLY and relies on file ownership to protect them.
// In bob's rootless store (one uid mapped, `build-users-group =`) the builder's uid owns the
// whole store, so a build could chmod and rewrite its inputs and thereby every later build.
//
// bob-builder therefore runs its job in a nested user + mount namespace in which every store
// mount is remounted read-only before any job code starts:
//
//  1. outer (pid 1 of Nix's sandbox): starts argv0 lockingArgv0 with
//     CLONE_NEWUSER|CLONE_NEWNS, its own uid and gid mapped to themselves (the build keeps its
//     uid; outputs are owned as before) and CAP_SYS_ADMIN as an ambient capability for the
//     remount, then makes itself non-dumpable, so nothing in the nested namespace can reach the
//     writable mounts through /proc/1/root, /proc/1/fd or ptrace, and releases the child;
//  2. locking: waits for the release, remounts every mount below the store directory read-only (MS_RDONLY added to
//     the flags the mount already has), checks the result, clears its ambient capabilities and
//     executes itself again as lockedArgv0, which drops every capability (non-root uid);
//  3. locked: checks again that no store mount is writable and that it holds no capability,
//     then runs the job (Main).
//
// The store directory itself stays writable: outputs are created in it. A builder started
// outside a sandbox (no mount below the store directory) runs the job directly.

const (
	lockingArgv0 = "bob-builder(locking-inputs)"
	lockedArgv0  = "bob-builder(inputs-read-only)"
)

// Linux constants the syscall package lacks.
const (
	prSetDumpable        = 4
	prCapAmbient         = 47
	prCapAmbientClearAll = 4
	capSysAdmin          = 21
	stRelatime           = 0x1000 // statvfs ST_RELATIME
	msRelatime           = 1 << 21
)

// LockInputs makes the inputs of the job read-only (see above). argv is os.Args. It returns
// done = false when the caller is to run the job now, in this process; otherwise the process
// exits with code (the job's exit status, or ExitStep when the inputs could not be locked).
func LockInputs(argv []string, stderr io.Writer) (code int, done bool) {
	store := os.Getenv("NIX_STORE")
	if store == "" {
		store = "/nix/store"
	}
	fail := func(err error) (int, bool) {
		// The last line names what failed (R-NIX-017).
		fmt.Fprintf(stderr, "bob-builder: cannot make the build inputs read-only: %s\n", oneLine(err.Error()))
		return ExitStep, true
	}
	argv0 := ""
	if len(argv) > 0 {
		argv0 = argv[0]
	}
	switch argv0 {
	case lockingArgv0:
		if err := awaitRelease(); err != nil {
			return fail(err)
		}
		if err := remountReadOnly(store); err != nil {
			return fail(err)
		}
		return fail(execLocked(argv))
	case lockedArgv0:
		if err := checkLocked(store); err != nil {
			return fail(err)
		}
		return 0, false
	}
	mounts, err := storeMounts(store)
	if err != nil {
		return fail(err)
	}
	writable := false
	for _, m := range mounts {
		writable = writable || !m.ro
	}
	if !writable {
		return 0, false
	}
	exit, err := runLocking(argv)
	if err != nil {
		return fail(err)
	}
	return exit, true
}

// runLocking starts the locking stage in a nested user and mount namespace, forwards
// termination signals to it and returns its exit status (128 + signal when it was killed).
func runLocking(argv []string) (int, error) {
	// The locking stage waits for one byte on fd 3: it goes on only once this process is
	// non-dumpable. That cannot happen before the start: the child's /proc files would then
	// belong to root and its uid map could not be written.
	r, w, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	defer w.Close()
	uid, gid := os.Getuid(), os.Getgid()
	cmd := exec.Command("/proc/self/exe")
	cmd.Args = append([]string{lockingArgv0}, argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{r}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{capSysAdmin},
		Pdeathsig:                  syscall.SIGKILL,
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	// Pdeathsig belongs to the thread that forks: keep it until the child is gone.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("nested user namespace: %w", err)
	}
	r.Close()
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); e != 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 0, fmt.Errorf("prctl(PR_SET_DUMPABLE): %w", e)
	}
	if _, err := w.Write([]byte{1}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 0, fmt.Errorf("release the locking stage: %w", err)
	}
	w.Close()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("wait: panic: %v", r)
			}
		}()
		done <- cmd.Wait()
	}()
	for {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case err := <-done:
			var ee *exec.ExitError
			if err != nil && !errors.As(err, &ee) {
				return 0, err
			}
			ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if ws.Signaled() {
				return 128 + int(ws.Signal()), nil
			}
			return ws.ExitStatus(), nil
		}
	}
}

// awaitRelease reads the byte runLocking writes to fd 3 once it is non-dumpable.
func awaitRelease() error {
	f := os.NewFile(3, "bob-builder-release")
	if f == nil {
		return errors.New("no release pipe")
	}
	defer f.Close()
	var b [1]byte
	if n, err := f.Read(b[:]); n != 1 {
		return fmt.Errorf("release pipe: %v", err)
	}
	return nil
}

// remountReadOnly adds MS_RDONLY to every mount below store and checks the result.
func remountReadOnly(store string) error {
	mounts, err := storeMounts(store)
	if err != nil {
		return err
	}
	for _, m := range mounts {
		if m.ro {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(m.path, &st); err != nil {
			return fmt.Errorf("statfs %s: %w", m.path, err)
		}
		if err := syscall.Mount("", m.path, "", remountFlags(st.Flags), ""); err != nil {
			return fmt.Errorf("remount %s read-only: %w", m.path, err)
		}
	}
	return checkMounts(store)
}

// remountFlags are the flags of a read-only bind remount that keeps the mount's other
// flags: the kernel refuses to clear flags that are locked by a less privileged namespace.
func remountFlags(statfs int64) uintptr {
	f := uintptr(syscall.MS_REMOUNT | syscall.MS_BIND | syscall.MS_RDONLY)
	for _, m := range []struct {
		st int64
		ms uintptr
	}{
		{syscall.MS_NOSUID, syscall.MS_NOSUID}, {syscall.MS_NODEV, syscall.MS_NODEV},
		{syscall.MS_NOEXEC, syscall.MS_NOEXEC}, {syscall.MS_SYNCHRONOUS, syscall.MS_SYNCHRONOUS},
		{syscall.MS_MANDLOCK, syscall.MS_MANDLOCK}, {syscall.MS_NOATIME, syscall.MS_NOATIME},
		{syscall.MS_NODIRATIME, syscall.MS_NODIRATIME}, {stRelatime, msRelatime},
	} {
		// statvfs ST_* values equal MS_* up to ST_NODIRATIME; ST_RELATIME differs.
		if statfs&m.st != 0 {
			f |= m.ms
		}
	}
	return f
}

// execLocked clears the ambient capabilities of this thread and executes the builder again
// as lockedArgv0; with a non-root uid, execve then leaves it no capability at all.
func execLocked(argv []string) error {
	runtime.LockOSThread() // prctl and execve must happen on the same thread
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0); e != 0 {
		return fmt.Errorf("prctl(PR_CAP_AMBIENT_CLEAR_ALL): %w", e)
	}
	if os.Getuid() == 0 {
		return errors.New("the build runs as root in the nested namespace")
	}
	return fmt.Errorf("exec: %w", syscall.Exec("/proc/self/exe", append([]string{lockedArgv0}, argv[1:]...), os.Environ()))
}

// checkLocked is the locked stage's check: no writable store mount, no capability.
func checkLocked(store string) error {
	if err := checkMounts(store); err != nil {
		return err
	}
	eff, err := effectiveCaps()
	if err != nil {
		return err
	}
	if eff != 0 {
		return fmt.Errorf("the build still holds capabilities %#x", eff)
	}
	return nil
}

// checkMounts fails when a mount below store is writable.
func checkMounts(store string) error {
	mounts, err := storeMounts(store)
	if err != nil {
		return err
	}
	for _, m := range mounts {
		if !m.ro {
			return fmt.Errorf("%s is still writable", m.path)
		}
	}
	return nil
}

// mount is one mount point below the store directory.
type mount struct {
	path string
	ro   bool
}

// storeMounts lists the mount points strictly below store from /proc/self/mountinfo.
func storeMounts(store string) ([]mount, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseMountinfo(f, store)
}

// parseMountinfo reads mountinfo lines (proc(5)): field 5 is the mount point (octal escapes),
// field 6 the per-mount options, of which "ro" marks a read-only mount.
func parseMountinfo(r io.Reader, store string) ([]mount, error) {
	prefix := filepath.Clean(store) + "/"
	var out []mount
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 {
			return nil, fmt.Errorf("mountinfo: malformed line %q", sc.Text())
		}
		p, err := unescapeMount(f[4])
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		ro := false
		for o := range strings.SplitSeq(f[5], ",") {
			ro = ro || o == "ro"
		}
		out = append(out, mount{path: p, ro: ro})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("mountinfo: %w", err)
	}
	return out, nil
}

// unescapeMount decodes the \ooo escapes the kernel writes for space, tab, newline and \.
func unescapeMount(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			if i+4 > len(s) {
				return "", fmt.Errorf("mountinfo: bad escape in %q", s)
			}
			n, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
			if err != nil {
				return "", fmt.Errorf("mountinfo: bad escape in %q", s)
			}
			b.WriteByte(byte(n))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

// effectiveCaps reads CapEff of this process.
func effectiveCaps() (uint64, error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	return 0, errors.New("/proc/self/status has no CapEff")
}
