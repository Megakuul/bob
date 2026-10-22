package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// launchFile is .bob/launch.json (SPEC R-OUT-015), written by bob-builder's appimage kind.
type launchFile struct {
	Schema       string            `json:"schema"`
	Entry        string            `json:"entry"`
	Entries      map[string]string `json:"entries"`
	Interp       string            `json:"interp"`
	LibDirs      []string          `json:"libDirs"`
	Env          []string          `json:"env"`
	Mode         string            `json:"mode"` // auto | userns
	HostGraphics bool              `json:"hostGraphics"`
}

const (
	launchSchema = "bob.appimage.launch/v1"
	storeDir     = "/nix/store"
	mountDir     = ".bob/mnt" // empty directory of the extracted bundle: the new root in userns mode
	stage2Env    = "BOBRUN_STAGE2"
	capSysChroot = 18
	capSysAdmin  = 21
)

func readLaunch(dir string) (*launchFile, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".bob", "launch.json"))
	if err != nil {
		return nil, err
	}
	var l launchFile
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.Schema != launchSchema {
		return nil, fmt.Errorf("schema %q, this runtime reads %s", l.Schema, launchSchema)
	}
	if !strings.HasPrefix(l.Entry, storeDir+"/") {
		return nil, fmt.Errorf("entry %q is not a store path", l.Entry)
	}
	return &l, nil
}

// launch starts the bundle extracted at dir.
func launch(dir, appimage string, args []string) int {
	l, err := readLaunch(dir)
	if err != nil {
		return fail("read launch.json: %v", err)
	}
	entry := l.Entry
	if name := os.Getenv("BOB_ENTRY"); name != "" {
		e, ok := l.Entries[name]
		if !ok {
			return fail("unknown entry %q", name)
		}
		entry = e
	} else if e, ok := l.Entries[filepath.Base(args[0])]; ok {
		entry = e
	}
	debug := os.Getenv("BOB_APPIMAGE_DEBUG") == "1"
	mode := os.Getenv("BOB_APPIMAGE_MODE")
	switch mode {
	case "":
		mode = l.Mode
	case "auto", "userns", "ldso":
	default:
		return fail("unknown BOB_APPIMAGE_MODE %q (auto, userns or ldso)", mode)
	}
	needsUserns := l.Mode == "userns" // appimage.UsernsOnly (R-OUT-019)
	store := filepath.Join(dir, "nix", "store")
	env := os.Environ()
	if appimage != "" {
		env = setEnv(env, "APPIMAGE="+appimage)
	}
	env = setEnv(env, "APPDIR="+dir)
	env = setEnv(env, "ARGV0="+args[0])

	if mode != "ldso" || needsUserns {
		if debug {
			fmt.Fprintln(os.Stderr, "bobrun: mode userns")
		}
		code, err := runUserns(dir, l, entry, args, env)
		if err == nil {
			return code
		}
		switch {
		case needsUserns:
			if debug {
				fmt.Fprintf(os.Stderr, "bobrun: user namespace: %v\n", err)
			}
			return fail("this bundle needs user namespaces")
		case mode == "userns":
			return fail("user namespace: %v", err)
		}
		if debug {
			fmt.Fprintf(os.Stderr, "bobrun: user namespace: %v; falling back to mode ldso\n", err)
		}
	}
	if debug {
		fmt.Fprintln(os.Stderr, "bobrun: mode ldso")
	}
	return runLdso(store, l, entry, args, env)
}

// setEnv replaces or appends one KEY=VALUE entry.
func setEnv(env []string, kv string) []string {
	k, _, _ := strings.Cut(kv, "=")
	for i, e := range env {
		if strings.HasPrefix(e, k+"=") {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}

// bundleEnv applies launch.json's env with ${STORE} expanded to store.
func bundleEnv(env []string, l *launchFile, store string) []string {
	for _, e := range l.Env {
		env = setEnv(env, strings.ReplaceAll(e, "${STORE}", store))
	}
	return env
}

// hostGraphicsDirs are the host's GPU library directories that exist (R-OUT-020).
func hostGraphicsDirs() []string {
	multiarch := map[string]string{"arm64": "aarch64-linux-gnu", "amd64": "x86_64-linux-gnu"}[runtime.GOARCH]
	var out []string
	for _, d := range []string{"/run/opengl-driver/lib", "/usr/lib/" + multiarch, "/usr/lib64", "/usr/lib"} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// runChild runs cmd, forwards SIGINT, SIGTERM, SIGHUP and SIGQUIT to it and returns its exit
// code (128+signal when it was killed by a signal).
func runChild(cmd *exec.Cmd, started func() error) (int, error) {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if started != nil {
		if err := started(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return 0, err
		}
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if st.Signaled() {
			return 128 + int(st.Signal()), nil
		}
		return st.ExitStatus(), nil
	}
	if err != nil {
		return 127, nil
	}
	return 0, nil
}

// stage2Req is what the namespace child receives (JSON in $BOBRUN_STAGE2).
type stage2Req struct {
	Dir   string   `json:"dir"`   // extracted bundle
	Cwd   string   `json:"cwd"`   // the caller's working directory
	Entry string   `json:"entry"` // /nix/store/… program
	Args  []string `json:"args"`  // argv[1:]
	Env   []string `json:"env"`
}

// runUserns starts the program in a new user and mount namespace. A returned error means the
// namespace could not be set up (nothing of the program ran).
func runUserns(dir string, l *launchFile, entry string, args, env []string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cwd, _ := os.Getwd()
	env = bundleEnv(env, l, storeDir)
	if l.HostGraphics {
		libs := append(append([]string{}, l.LibDirs...), hostGraphicsDirs()...)
		env = setEnv(env, "LD_LIBRARY_PATH="+strings.Join(libs, ":"))
	}
	req, err := json.Marshal(stage2Req{Dir: dir, Cwd: cwd, Entry: entry, Args: args[1:], Env: env})
	if err != nil {
		return 0, err
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer rd.Close()
	defer wr.Close()
	cmd := exec.Command(self)
	cmd.Args = []string{args[0]}
	cmd.Env = append(os.Environ(), stage2Env+"="+string(req))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{wr} // fd 3: setup errors; closed by the exec of the program
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		// The setup (mount, chroot) runs after the exec of this binary as the caller's uid, so it
		// needs ambient capabilities; stage2 clears them before it execs the program.
		AmbientCaps: []uintptr{capSysChroot, capSysAdmin},
	}
	return runChild(cmd, func() error {
		wr.Close()
		msg, _ := io.ReadAll(io.LimitReader(rd, 4096))
		if len(msg) > 0 {
			return errors.New(strings.TrimSpace(string(msg)))
		}
		return nil
	})
}

// stage2 runs inside the new namespaces: a tmpfs root holding every top-level entry of the
// host except /nix, the bundled store at /nix/store, chroot, then exec of the program.
func stage2(raw string) int {
	report := os.NewFile(3, "setup")
	setupFail := func(err error) int {
		if report != nil {
			fmt.Fprintln(report, err)
		}
		return 127
	}
	var req stage2Req
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return setupFail(err)
	}
	if err := enterRoot(req.Dir, req.Cwd); err != nil {
		return setupFail(err)
	}
	// No ambient capabilities survive into the program (PR_CAP_AMBIENT_CLEAR_ALL).
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, 47, 4, 0, 0, 0, 0); e != 0 && e != syscall.EINVAL {
		return setupFail(fmt.Errorf("clear ambient capabilities: %v", e))
	}
	syscall.CloseOnExec(3)
	argv := append([]string{path.Base(req.Entry)}, req.Args...)
	err := syscall.Exec(req.Entry, argv, req.Env)
	// The program could not be started: an error of the bundle, not of the namespace.
	fmt.Fprintf(os.Stderr, "bobrun: cannot run %s: %v\n", path.Base(req.Entry), err)
	if report != nil {
		report.Close()
	}
	return 127
}

func enterRoot(dir, cwd string) error {
	root := filepath.Join(dir, mountDir)
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := syscall.Mount("tmpfs", root, "tmpfs", syscall.MS_NODEV|syscall.MS_NOSUID, "mode=0755"); err != nil {
		return fmt.Errorf("mount tmpfs: %w", err)
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
			t, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(t, dst); err != nil {
				return err
			}
			continue
		case fi.IsDir():
			if err := os.Mkdir(dst, 0o755); err != nil {
				return err
			}
		default:
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			f.Close()
		}
		if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOENT) {
				continue
			}
			return fmt.Errorf("bind %s: %w", src, err)
		}
	}
	store := filepath.Join(root, "nix", "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(filepath.Join(dir, "nix", "store"), store, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind the bundled store: %w", err)
	}
	if err := syscall.Chroot(root); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if cwd == "" || syscall.Chdir(cwd) != nil {
		return syscall.Chdir("/")
	}
	return nil
}

// runLdso runs the program through the bundled dynamic loader with the bundle's library
// directories; ${STORE} is the extracted store.
func runLdso(store string, l *launchFile, entry string, args, env []string) int {
	prog, err := resolve(store, entry)
	if err != nil {
		return fail("entry %s: %v", entry, err)
	}
	env = bundleEnv(env, l, store)
	interp, static, err := interpOf(prog)
	if err != nil {
		return fail("entry %s: %v", entry, err)
	}
	var argv []string
	if static {
		argv = append([]string{prog}, args[1:]...)
	} else {
		if interp == "" {
			interp = l.Interp
		}
		ld, err := resolve(store, interp)
		if err != nil {
			return fail("loader %s: %v", interp, err)
		}
		var libs []string
		for _, d := range l.LibDirs {
			libs = append(libs, mapStore(store, d))
		}
		if l.HostGraphics {
			libs = append(libs, hostGraphicsDirs()...)
		}
		if h := os.Getenv("LD_LIBRARY_PATH"); h != "" {
			libs = append(libs, h)
		}
		argv = append([]string{ld, "--argv0", path.Base(entry), "--library-path", strings.Join(libs, ":"), prog}, args[1:]...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if static {
		cmd.Args[0] = path.Base(entry) // multi-call programs dispatch on argv[0]
	}
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	code, err := runChild(cmd, nil)
	if err != nil {
		return fail("cannot run %s: %v", path.Base(entry), err)
	}
	return code
}

// mapStore turns /nix/store/… into the same path below the extracted store.
func mapStore(store, p string) string {
	if rest, ok := strings.CutPrefix(p, storeDir+"/"); ok {
		return filepath.Join(store, rest)
	}
	return p
}

// resolve follows symlinks of a /nix/store path inside the extracted store (absolute targets
// in /nix/store are mapped, nothing outside the store is followed).
func resolve(store, p string) (string, error) {
	cur := mapStore(store, p)
	for range 40 {
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return cur, nil
		}
		t, err := os.Readlink(cur)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(t) {
			if !strings.HasPrefix(t, storeDir+"/") {
				return "", fmt.Errorf("links outside the bundle (%s)", t)
			}
			cur = mapStore(store, t)
		} else {
			cur = filepath.Join(filepath.Dir(cur), t)
		}
	}
	return "", errors.New("too many levels of symbolic links")
}

// interpOf reads the PT_INTERP of an ELF64 file; static is true for an ELF without one.
func interpOf(file string) (interp string, static bool, err error) {
	f, err := os.Open(file)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	var h [64]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:4]) != "\x7fELF" {
		return "", false, errors.New("not an ELF executable (a script needs user namespaces)")
	}
	le := binary.LittleEndian
	phoff, phentsize, phnum := int64(le.Uint64(h[32:])), int64(le.Uint16(h[54:])), int64(le.Uint16(h[56:]))
	if phentsize < 56 || phnum > 128 {
		return "", false, errors.New("bad program headers")
	}
	ph := make([]byte, phentsize*phnum)
	if _, err := f.ReadAt(ph, phoff); err != nil {
		return "", false, err
	}
	for i := int64(0); i < phnum; i++ {
		p := ph[i*phentsize:]
		if le.Uint32(p) != 3 { // PT_INTERP
			continue
		}
		off, size := int64(le.Uint64(p[8:])), int64(le.Uint64(p[32:]))
		if size <= 0 || size > 4096 {
			return "", false, errors.New("bad PT_INTERP")
		}
		b := make([]byte, size)
		if _, err := f.ReadAt(b, off); err != nil {
			return "", false, err
		}
		return strings.TrimRight(string(b), "\x00"), false, nil
	}
	return "", true, nil
}
