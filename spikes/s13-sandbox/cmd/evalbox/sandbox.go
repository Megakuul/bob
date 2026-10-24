package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const evalUID = 1000 // uid of the evaluator inside the nested user namespace

func must(err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func bindRO(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		f.Close()
	}
	if err := unix.Mount(src, dst, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s: %w", src, err)
	}
	// Read-only is load-bearing: in a rootless store the evaluator's kernel uid owns the files.
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV)
	if err := unix.Mount("", dst, "", flags, ""); err != nil {
		// Locked flags of the original mount (e.g. noexec) must be preserved.
		var st unix.Statfs_t
		if e2 := unix.Statfs(dst, &st); e2 == nil && st.Flags&unix.ST_NOEXEC != 0 {
			flags |= unix.MS_NOEXEC
		}
		if err := unix.Mount("", dst, "", flags, ""); err != nil {
			return fmt.Errorf("remount ro %s: %w", dst, err)
		}
	}
	return nil
}

func stage1(cj string) error {
	var cfg Config
	if err := json.Unmarshal([]byte(cj), &cfg); err != nil {
		return err
	}
	_ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return must(err, "make / private")
	}
	base := "/mnt"
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		base = "/tmp"
	}
	R := base
	if err := unix.Mount("tmpfs", R, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=16m,mode=755"); err != nil {
		return must(err, "root tmpfs")
	}
	for _, p := range cfg.StorePaths {
		if err := bindRO(p, R+p); err != nil {
			return err
		}
	}
	if cfg.Src != "" {
		if err := bindRO(cfg.Src, R+"/src"); err != nil {
			return err
		}
	}
	if cfg.ModCache != "" {
		if err := bindRO(cfg.ModCache, R+"/gomod"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(R+"/work", 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", R+"/work", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size="+cfg.WorkSize+",mode=755"); err != nil {
		return must(err, "work tmpfs")
	}
	for _, d := range []string{"home", "tmp", "gocache", ".ov/up", ".ov/wk"} {
		if err := os.MkdirAll(R+"/work/"+d, 0o755); err != nil {
			return err
		}
	}
	if cfg.SeedCache != "" {
		// Trusted warm build cache as the lower layer; this evaluation's writes go to its own
		// tmpfs upper layer, so one evaluation can never poison the cache of another.
		opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", cfg.SeedCache, R+"/work/.ov/up", R+"/work/.ov/wk")
		if err := unix.Mount("overlay", R+"/work/gocache", "overlay", unix.MS_NOSUID|unix.MS_NODEV, opts); err != nil {
			return must(err, "gocache overlay")
		}
	}
	// /dev: a tmpfs with a few bind-mounted host device nodes.
	if err := os.MkdirAll(R+"/dev", 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", R+"/dev", "tmpfs", unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k,mode=755"); err != nil {
		return must(err, "dev tmpfs")
	}
	for _, d := range []string{"null", "zero", "full", "random", "urandom"} {
		f, err := os.Create(R + "/dev/" + d)
		if err != nil {
			return err
		}
		f.Close()
		if err := unix.Mount("/dev/"+d, R+"/dev/"+d, "", unix.MS_BIND, ""); err != nil {
			return must(err, "bind /dev/"+d)
		}
	}
	for n, t := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		_ = os.Symlink(t, R+"/dev/"+n)
	}
	if err := os.MkdirAll(R+"/proc", 0o555); err != nil {
		return err
	}
	if err := unix.Mount("proc", R+"/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "hidepid=2"); err != nil {
		return must(err, "mount proc")
	}
	if err := os.MkdirAll(R+"/etc", 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"passwd": fmt.Sprintf("bob:x:%d:%d:bob:/work/home:/noshell\nnobody:x:65534:65534::/:/noshell\n", evalUID, evalUID),
		"group":  fmt.Sprintf("bob:x:%d:\nnobody:x:65534:\n", evalUID),
		"hosts":  "127.0.0.1 localhost\n::1 localhost\n",
	}
	for n, c := range files {
		if err := os.WriteFile(R+"/etc/"+n, []byte(c), 0o644); err != nil {
			return err
		}
	}
	_ = os.Symlink("/work/tmp", R+"/tmp")
	if err := os.MkdirAll(R+"/.old", 0o700); err != nil {
		return err
	}
	if err := unix.PivotRoot(R, R+"/.old"); err != nil {
		return must(err, "pivot_root")
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	if err := unix.Unmount("/.old", unix.MNT_DETACH); err != nil {
		return must(err, "detach old root")
	}
	_ = os.Remove("/.old")
	if err := unix.Mount("", "/", "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return must(err, "remount / ro")
	}
	_ = unix.Sethostname([]byte("bob-eval"))
	// Only one nested user namespace may ever exist below this one: stage2's. Kernel-enforced,
	// independent of seccomp.
	if err := os.WriteFile("/proc/sys/user/max_user_namespaces", []byte("1"), 0); err != nil {
		fmt.Fprintln(os.Stderr, "evalbox: warning: cannot cap nested user namespaces:", err)
	}
	if cfg.Timing {
		fmt.Fprintf(os.Stderr, "evalbox: stage1 ready after %.2f ms\n", float64(time.Now().UnixNano()-cfg.Start)/1e6)
	}

	cmd := exec.Command("/proc/self/exe", stage2Marker, cj)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{os.NewFile(3, "graph")}
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: evalUID, HostID: 0, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: evalUID, HostID: 0, Size: 1}},
		GidMappingsEnableSetgroups: false,
		Pdeathsig:                  syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		return must(err, "start stage2")
	}
	os.NewFile(3, "graph").Close()
	// pid 1: reap everything until the evaluator itself exits; the kernel kills the rest of the
	// pid namespace when we return.
	target := cmd.Process.Pid
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return must(err, "wait")
		}
		if pid == target {
			if ws.Signaled() {
				os.Exit(128 + int(ws.Signal()))
			}
			os.Exit(ws.ExitStatus())
		}
	}
}

func stage2(cj string) error {
	var cfg Config
	if err := json.Unmarshal([]byte(cj), &cfg); err != nil {
		return err
	}
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return must(err, "no_new_privs")
	}
	if !cfg.NoLandlock {
		if err := applyLandlock(cfg); err != nil {
			return must(err, "landlock")
		}
	}
	if !cfg.NoSeccomp {
		if err := applySeccomp(); err != nil {
			return must(err, "seccomp")
		}
	}
	if err := os.Chdir(cfg.Dir); err != nil {
		return err
	}
	path := cfg.Argv[0]
	if !strings.Contains(path, "/") {
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, "PATH=") {
				os.Setenv("PATH", e[5:])
			}
		}
		p, err := exec.LookPath(path)
		if err != nil {
			return err
		}
		path = p
	}
	if cfg.Timing {
		fmt.Fprintf(os.Stderr, "evalbox: exec evaluator after %.2f ms\n", float64(time.Now().UnixNano()-cfg.Start)/1e6)
	}
	return unix.Exec(path, cfg.Argv, cfg.Env)
}
