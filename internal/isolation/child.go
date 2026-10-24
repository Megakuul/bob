package isolation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// caBundles are the host CA bundle locations bound into the container (TLS to the caches).
var caBundles = []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"}

// CABundle is where the container sees the host's CA bundle (NIX_SSL_CERT_FILE, SSL_CERT_FILE).
const CABundle = "/etc/ssl/certs/ca-certificates.crt"

// devNodes are the device nodes of the container's /dev.
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

// Child sets up the job container described by the JSON cfg and executes its program. It
// runs as pid 1 of the container's pid namespace, as root, in fresh mount, ipc, uts and cgroup
// namespaces. It only returns on failure, with the exit status for main to exit with.
func Child(cfg string) int {
	var c childConfig
	if err := json.Unmarshal([]byte(cfg), &c); err != nil {
		fmt.Fprintf(os.Stderr, "bob job container: configuration: %v\n", err)
		return ExitSetup
	}
	if err := setup(&c); err != nil {
		fmt.Fprintf(os.Stderr, "bob job container: %v\n", err)
		return ExitSetup
	}
	err := syscall.Exec(c.Args[0], c.Args, c.Env)
	fmt.Fprintf(os.Stderr, "bob job container: exec %s: %v\n", c.Args[0], err)
	return ExitSetup
}

func setup(c *childConfig) error {
	if len(c.Args) == 0 || c.Root == "" || c.Store == "" {
		return errors.New("incomplete configuration")
	}
	// Nothing mounted here propagates back to the host.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	r := c.Root
	if err := syscall.Mount("tmpfs", r, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755,size=64m"); err != nil {
		return fmt.Errorf("mount the container root: %w", err)
	}
	for _, d := range []string{"nix", "proc", "dev", "tmp", "etc", "bin", "sys/fs/cgroup", "work", "root", "run", "etc/ssl/certs", "etc/nix"} {
		if err := os.MkdirAll(filepath.Join(r, d), 0o755); err != nil {
			return err
		}
	}
	// The persistent store, read-write: Nix runs as root in here; builds run as allocated
	// non-root uids and cannot write the root-owned input paths.
	if err := bind(c.Store, filepath.Join(r, "nix"), true); err != nil {
		return fmt.Errorf("bind the store: %w", err)
	}
	for _, b := range c.Binds {
		if !filepath.IsAbs(b.Path) {
			return fmt.Errorf("bind target %q is not absolute", b.Path)
		}
		if err := bind(b.Host, filepath.Join(r, b.Path), b.Writable); err != nil {
			return fmt.Errorf("bind %s: %w", b.Path, err)
		}
	}
	for _, ca := range caBundles {
		if _, err := os.Stat(ca); err == nil {
			if err := bind(ca, filepath.Join(r, CABundle), false); err != nil {
				return fmt.Errorf("bind the CA bundle: %w", err)
			}
			break
		}
	}
	if err := syscall.Mount("tmpfs", filepath.Join(r, "tmp"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("mount /tmp: %w", err)
	}
	if err := devTree(filepath.Join(r, "dev")); err != nil {
		return err
	}
	if err := etcFiles(filepath.Join(r, "etc")); err != nil {
		return err
	}
	// The cgroup namespace is rooted at the job's leaf: Nix (use-cgroups) creates the
	// cgroups of its builds below it, never anywhere else.
	if err := syscall.Mount("cgroup2", filepath.Join(r, "sys/fs/cgroup"), "cgroup2", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount the job cgroup: %w", err)
	}
	if err := os.Chdir(r); err != nil {
		return err
	}
	// pivot_root(".", ".") stacks the old root under the new one; detaching it leaves only
	// the container's tree.
	if err := syscall.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := syscall.Unmount(".", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach the host root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	// A fresh /proc of the container's pid namespace: the worker's processes (and their
	// environments) are not in it.
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	host := c.Hostname
	if host == "" {
		host = "bob-job"
	}
	if err := syscall.Sethostname([]byte(host)); err != nil {
		return fmt.Errorf("set the host name: %w", err)
	}
	dir := c.Dir
	if dir == "" {
		dir = "/"
	}
	return os.Chdir(dir)
}

// bind mounts src on dst (creating dst as a file or directory like src), read-only unless rw,
// without device nodes.
func bind(src, dst string, rw bool) error { return bindFlags(src, dst, rw, syscall.MS_NODEV) }

func bindFlags(src, dst string, rw bool, extra uintptr) error {
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
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_ = f.Close()
	}
	if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return err
	}
	flags := syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | extra
	if !rw {
		flags |= syscall.MS_RDONLY
	}
	return syscall.Mount("", dst, "", flags, "")
}

// devTree mounts a small /dev: the usual character devices bound from the host, /dev/shm,
// /dev/pts and the fd links.
func devTree(dev string) error {
	if err := syscall.Mount("tmpfs", dev, "tmpfs", syscall.MS_NOSUID|syscall.MS_NOEXEC, "mode=0755,size=1m"); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}
	for _, n := range devNodes {
		if _, err := os.Stat("/dev/" + n); err != nil {
			continue
		}
		if err := bindFlags("/dev/"+n, filepath.Join(dev, n), true, syscall.MS_NOEXEC); err != nil {
			return fmt.Errorf("bind /dev/%s: %w", n, err)
		}
	}
	for _, d := range []string{"shm", "pts"} {
		if err := os.Mkdir(filepath.Join(dev, d), 0o755); err != nil {
			return err
		}
	}
	if err := syscall.Mount("tmpfs", filepath.Join(dev, "shm"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("mount /dev/shm: %w", err)
	}
	if err := syscall.Mount("devpts", filepath.Join(dev, "pts"), "devpts", syscall.MS_NOSUID|syscall.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"); err == nil {
		_ = os.Symlink("pts/ptmx", filepath.Join(dev, "ptmx"))
	}
	for name, target := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		if err := os.Symlink(target, filepath.Join(dev, name)); err != nil {
			return err
		}
	}
	return nil
}

// etcFiles writes a minimal /etc: root and nobody, localhost, the host's DNS resolvers.
func etcFiles(etc string) error {
	resolv, _ := os.ReadFile("/etc/resolv.conf")
	files := map[string]string{
		"passwd":        "root:x:0:0:root:/root:/bin/sh\nnobody:x:65534:65534:nobody:/:/noshell\n",
		"group":         "root:x:0:\nnixbld:x:30000:\nnogroup:x:65534:\n",
		"hosts":         "127.0.0.1 localhost\n::1 localhost\n",
		"nsswitch.conf": "hosts: files dns\n",
		"resolv.conf":   string(resolv),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(etc, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}
