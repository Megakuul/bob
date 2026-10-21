//go:build linux

// Command enter runs a command inside a distro rootfs as an unprivileged user, using only
// unprivileged user namespaces (HARNESS.md §7). It is the rootless matrix's sandbox; it needs
// no setuid helper, no bwrap and no host change.
//
// Three stages, each a re-exec of this binary:
//
//	stage 0  the calling user (bobtest): clones stage 1 into new user, mount and pid namespaces.
//	stage 1  root of that namespace (pid 1): overlays the rootfs on a scratch upper dir, mounts
//	         /proc, /dev, /tmp and the binds, pivots into the overlay, clones stage 2 into a
//	         nested user namespace whose only uid is the caller's uid.
//	stage 2  the caller's uid, as a plain user: with -block-userns writes
//	         user.max_user_namespaces=0 for its own namespace (the setting is per namespace,
//	         so the host is untouched; S0b), then drops every capability and execs the command.
//
// The nested namespace is what makes the simulation honest: the command runs without
// capabilities, as a non-root uid, and any user namespace it creates is a child of the one
// whose limit is 0.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const (
	envStage = "BOB_ENTER_STAGE"
	// ProbeUserns as the command makes stage 2 try to create a user namespace and print
	// "userns: ok" or "userns: <errno name>"; the runner uses it to check the simulation.
	probeUserns = "@probe-userns"

	// Not in package syscall; values from linux/capability.h and linux/prctl.h.
	capSysResource       = 24
	prCapAmbient         = 47
	prCapAmbientClearAll = 4
)

func errnoName(en syscall.Errno) string {
	switch en {
	case syscall.ENOSPC:
		return "ENOSPC"
	case syscall.EPERM:
		return "EPERM"
	case syscall.EACCES:
		return "EACCES"
	case syscall.EINVAL:
		return "EINVAL"
	}
	return en.Error()
}

type bindList []string

func (b *bindList) String() string     { return strings.Join(*b, ",") }
func (b *bindList) Set(v string) error { *b = append(*b, v); return nil }

type opts struct {
	rootfs, scratch, cwd string
	uid, gid             int
	binds, robinds, envs bindList
	blockUserns, nixos   bool
	args                 []string
}

func parse(args []string) (*opts, error) {
	o := &opts{}
	fl := flag.NewFlagSet("enter", flag.ContinueOnError)
	fl.StringVar(&o.rootfs, "rootfs", "", "extracted rootfs (read-only lower layer)")
	fl.StringVar(&o.scratch, "scratch", "", "empty directory for the overlay upper and work dirs")
	fl.StringVar(&o.cwd, "cwd", "/", "working directory inside")
	fl.IntVar(&o.uid, "uid", -1, "uid inside (default: the caller's)")
	fl.IntVar(&o.gid, "gid", -1, "gid inside (default: the caller's)")
	fl.Var(&o.binds, "bind", "host:guest read-write bind (repeatable)")
	fl.Var(&o.robinds, "ro-bind", "host:guest read-only bind (repeatable)")
	fl.Var(&o.envs, "env", "K=V for the command; the environment is otherwise empty (repeatable)")
	fl.BoolVar(&o.blockUserns, "block-userns", false, "set user.max_user_namespaces=0 for the command")
	fl.BoolVar(&o.nixos, "nixos", false, "create /etc/NIXOS in the rootfs")
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	o.args = fl.Args()
	if o.rootfs == "" || o.scratch == "" || len(o.args) == 0 {
		return nil, errors.New("usage: enter -rootfs DIR -scratch DIR [flags] -- command [args]")
	}
	return o, nil
}

func main() {
	// Pdeathsig belongs to the thread that forked; keep the main goroutine on one thread.
	runtime.LockOSThread()
	o, err := parse(os.Args[1:])
	if err != nil {
		fail(err)
	}
	switch os.Getenv(envStage) {
	case "":
		err = stage0(o)
	case "1":
		err = stage1(o)
	case "2":
		err = stage2(o)
	default:
		os.Exit(0) // the probe's child: creating the namespace was the test
	}
	fail(err)
}

func fail(err error) {
	var ex *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &ex):
		if ws, ok := ex.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			os.Exit(128 + int(ws.Signal()))
		}
		os.Exit(ex.ExitCode())
	}
	fmt.Fprintf(os.Stderr, "enter: %v\n", err)
	os.Exit(120) // distinct from bob's exit codes: the runner reports it as a harness error
}

func self(stage string, o *opts, attr *syscall.SysProcAttr) *exec.Cmd {
	cmd := exec.Command("/proc/self/exe", os.Args[1:]...)
	cmd.Env = []string{envStage + "=" + stage}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	attr.Pdeathsig = syscall.SIGKILL
	cmd.SysProcAttr = attr
	return cmd
}

func stage0(o *opts) error {
	if o.uid < 0 {
		o.uid = os.Getuid()
	}
	if o.gid < 0 {
		o.gid = os.Getgid()
	}
	args := append([]string{"-uid", strconv.Itoa(o.uid), "-gid", strconv.Itoa(o.gid)}, os.Args[1:]...)
	os.Args = append(os.Args[:1], args...)
	cmd := self("1", o, &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	})
	// Signals go to stage 1 through its death; Ctrl-C on the runner kills the whole pid ns.
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	if err := cmd.Run(); err != nil {
		var ex *exec.ExitError
		if !errors.As(err, &ex) {
			return fmt.Errorf("create the outer user namespace: %w", err)
		}
		return err
	}
	return nil
}

func stage1(o *opts) error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make / private: %w", err)
	}
	upper, work, merged := filepath.Join(o.scratch, "upper"), filepath.Join(o.scratch, "work"), filepath.Join(o.scratch, "merged")
	for _, d := range []string{upper, work, merged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	data := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", o.rootfs, upper, work)
	if err := syscall.Mount("overlay", merged, "overlay", 0, data+",userxattr"); err != nil {
		if err2 := syscall.Mount("overlay", merged, "overlay", 0, data); err2 != nil {
			return fmt.Errorf("overlay mount: %w (with userxattr: %v)", err2, err)
		}
	}
	in := func(p string) string { return filepath.Join(merged, p) }
	if err := mkdirFresh(in("/proc")); err != nil {
		return err
	}
	if err := syscall.Mount("proc", in("/proc"), "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	if err := bind("/dev", in("/dev"), false); err != nil {
		return err
	}
	if err := mkdirFresh(in("/tmp")); err != nil {
		return err
	}
	if err := syscall.Mount("tmpfs", in("/tmp"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("mount /tmp: %w", err)
	}
	// DNS: base images often lack resolv.conf or point it at a systemd stub that is not running.
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = os.Remove(in("/etc/resolv.conf"))
		if err := os.WriteFile(in("/etc/resolv.conf"), b, 0o644); err != nil {
			return err
		}
	}
	if o.nixos {
		if err := os.WriteFile(in("/etc/NIXOS"), nil, 0o644); err != nil {
			return err
		}
	}
	for _, list := range []struct {
		l  bindList
		ro bool
	}{{o.binds, false}, {o.robinds, true}} {
		for _, b := range list.l {
			src, dst, ok := strings.Cut(b, ":")
			if !ok {
				return fmt.Errorf("bind %q: want host:guest", b)
			}
			if err := bind(src, in(dst), list.ro); err != nil {
				return err
			}
		}
	}
	if err := os.Chdir(merged); err != nil {
		return err
	}
	if err := syscall.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := syscall.Unmount(".", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach the old root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	attr := &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: o.uid, HostID: 0, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: o.gid, HostID: 0, Size: 1}},
	}
	if o.blockUserns {
		// Kept across the exec of stage 2 (a non-root uid) only to write the sysctl.
		attr.AmbientCaps = []uintptr{capSysResource}
	}
	return self("2", o, attr).Run()
}

func stage2(o *opts) error {
	if o.blockUserns {
		if err := os.WriteFile("/proc/sys/user/max_user_namespaces", []byte("0\n"), 0o644); err != nil {
			return fmt.Errorf("simulate user.max_user_namespaces=0: %w", err)
		}
	}
	if _, _, en := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0, 0, 0, 0); en != 0 {
		return fmt.Errorf("drop ambient capabilities: %w", en)
	}
	env := []string(o.envs)
	path := "/usr/bin:/bin"
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
		}
		if v, ok := strings.CutPrefix(e, "HOME="); ok {
			_ = os.MkdirAll(v, 0o755)
		}
	}
	if err := os.Chdir(o.cwd); err != nil {
		return err
	}
	if o.args[0] == probeUserns {
		cmd := exec.Command("/proc/self/exe", os.Args[1:]...)
		cmd.Env = []string{envStage + "=probe"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
		res := "ok"
		if err := cmd.Run(); err != nil {
			var en syscall.Errno
			if errors.As(err, &en) {
				res = errnoName(en)
			} else {
				res = err.Error()
			}
		}
		fmt.Printf("userns: %s\nuid: %d\n", res, os.Getuid())
		return nil
	}
	os.Setenv("PATH", path)
	bin, err := exec.LookPath(o.args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(bin, o.args, env)
}

// mkdirFresh makes sure p is a directory (a rootfs may ship /tmp as a symlink or lack /proc).
func mkdirFresh(p string) error {
	if fi, err := os.Lstat(p); err == nil && !fi.IsDir() {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return os.MkdirAll(p, 0o755)
}

func bind(src, dst string, ro bool) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := mkdirFresh(dst); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, nil, 0o644); err != nil {
			return err
		}
	}
	if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s: %w", src, err)
	}
	if ro {
		if err := syscall.Mount("", dst, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("read-only %s: %w", src, err)
		}
	}
	return nil
}
