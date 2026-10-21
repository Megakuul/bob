// bobrun is the AppRun launcher inside a bob AppImage (spike). It starts the bundled
// program so that its absolute /nix/store paths resolve, in one of two ways:
//
//	userns: new user+mount namespace, a tmpfs root that bind-mounts every top-level
//	        host directory, plus $APPDIR/nix/store at /nix/store, then chroot + exec.
//	        Everything works (data paths, child processes, /proc/self/exe).
//	ldso:   no namespaces: exec the bundled ld.so with --library-path. Libraries resolve;
//	        absolute data paths and exec of other store paths do not.
//
// BOB_APPIMAGE_MODE=auto|userns|ldso (default auto: userns, falling back to ldso).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type launch struct {
	Entry   string            `json:"entry"`
	Entries map[string]string `json:"entries"`
	Interp  string            `json:"interp"`
	LibDirs []string          `json:"libDirs"`
	Env     []string          `json:"env"`
}

const nixStore = "/nix/store"

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bobrun:", err)
		os.Exit(127)
	}
	os.Exit(code)
}

func debugf(f string, a ...any) {
	if os.Getenv("BOB_APPIMAGE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "bobrun: "+f+"\n", a...)
	}
}

func run() (int, error) {
	appdir := os.Getenv("APPDIR")
	if appdir == "" {
		exe, err := os.Executable()
		if err != nil {
			return 0, err
		}
		appdir = filepath.Dir(exe)
	}
	b, err := os.ReadFile(filepath.Join(appdir, ".bob", "launch.json"))
	if err != nil {
		return 0, fmt.Errorf("read launch.json: %w", err)
	}
	var l launch
	if err := json.Unmarshal(b, &l); err != nil {
		return 0, fmt.Errorf("parse launch.json: %w", err)
	}
	// Entry selection: BOB_ENTRY, else the name the AppImage was started as (busybox-style
	// symlink, ARGV0 is set by the runtime), else the main entry.
	e := os.Getenv("BOB_ENTRY")
	if e == "" {
		if _, ok := l.Entries[filepath.Base(os.Getenv("ARGV0"))]; ok {
			e = filepath.Base(os.Getenv("ARGV0"))
		}
	}
	if e != "" {
		p, ok := l.Entries[e]
		if !ok {
			return 0, fmt.Errorf("unknown entry %q", e)
		}
		l.Entry = p
	}
	store := filepath.Join(appdir, "nix", "store")
	argv0 := filepath.Base(l.Entry)
	if os.Getenv("_BOBRUN_CHILD") == "1" {
		return 0, child(store, l, argv0)
	}
	mode := os.Getenv("BOB_APPIMAGE_MODE")
	if mode == "" {
		mode = "auto"
	}
	if mode == "auto" || mode == "userns" {
		code, err := parent(store)
		if err == nil {
			return code, nil
		}
		if mode == "userns" {
			return 0, fmt.Errorf("user namespace: %w", err)
		}
		debugf("user namespace unavailable (%v), using ld.so mode", err)
	}
	return 0, ldso(store, l, argv0)
}

// parent re-executes this launcher in a new user and mount namespace.
func parent(store string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	root, err := os.MkdirTemp("", "bobrun-root-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(root)
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "_BOBRUN_CHILD=1", "_BOBRUN_ROOT="+root)
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		// keep CAP_SYS_ADMIN/CAP_SYS_CHROOT across exec as non-root uid inside the userns
		AmbientCaps:                []uintptr{21, 18},
		GidMappingsEnableSetgroups: false,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	return 0, err
}

// child runs inside the namespaces: build the new root, chroot, exec.
func child(store string, l launch, argv0 string) error {
	root := os.Getenv("_BOBRUN_ROOT")
	cwd, _ := os.Getwd()
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make / private: %w", err)
	}
	if err := syscall.Mount("tmpfs", root, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("tmpfs: %w", err)
	}
	ents, err := os.ReadDir("/")
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if name == "nix" {
			continue
		}
		src, dst := "/"+name, filepath.Join(root, name)
		fi, err := os.Lstat(src)
		if err != nil {
			continue
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, _ := os.Readlink(src)
			if err := os.Symlink(t, dst); err != nil {
				return err
			}
			continue
		case fi.IsDir():
			if err := os.Mkdir(dst, 0o755); err != nil {
				return err
			}
		default:
			if err := os.WriteFile(dst, nil, 0o644); err != nil {
				return err
			}
		}
		if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			debugf("bind %s: %v (skipped)", src, err)
		}
	}
	ns := filepath.Join(root, "nix", "store")
	if err := os.MkdirAll(ns, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(store, ns, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind store: %w", err)
	}
	if err := syscall.Chroot(root); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := os.Chdir(cwd); err != nil {
		_ = os.Chdir("/")
	}
	// drop the ambient capabilities before running the program
	_, _, _ = syscall.RawSyscall6(syscall.SYS_PRCTL, 47 /*PR_CAP_AMBIENT*/, 4 /*CLEAR_ALL*/, 0, 0, 0, 0)
	env := cleanEnv(expand(l.Env, nixStore))
	debugf("userns: exec %s", l.Entry)
	return syscall.Exec(l.Entry, append([]string{argv0}, os.Args[1:]...), env)
}

func ldso(store string, l launch, argv0 string) error {
	reloc := func(p string) string { return store + strings.TrimPrefix(p, nixStore) }
	env := cleanEnv(expand(l.Env, store))
	if l.Interp == "" {
		return syscall.Exec(reloc(l.Entry), append([]string{argv0}, os.Args[1:]...), env)
	}
	var dirs []string
	for _, d := range l.LibDirs {
		dirs = append(dirs, reloc(d))
	}
	// --library-path replaces LD_LIBRARY_PATH; keep the host's value after the bundle's dirs
	// (needed for host GPU drivers, see S8 write-up).
	if h := os.Getenv("LD_LIBRARY_PATH"); h != "" {
		dirs = append(dirs, h)
	}
	args := []string{reloc(l.Interp), "--library-path", strings.Join(dirs, ":"), "--argv0", argv0, reloc(l.Entry)}
	args = append(args, os.Args[1:]...)
	debugf("ldso: exec %v", args[:1])
	return syscall.Exec(args[0], args, env)
}

func expand(vars []string, store string) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = strings.ReplaceAll(v, "${STORE}", store)
	}
	return out
}

// cleanEnv: host environment plus the bundle's variables (bundle wins), minus our own.
func cleanEnv(add []string) []string {
	m := map[string]string{}
	var order []string
	for _, kv := range append(os.Environ(), add...) {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "_BOBRUN_") {
			continue
		}
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = kv
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, m[k])
	}
	return out
}
