package rootless

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
)

// The rootless matrix (HARNESS.md §7.1) runs a static bob as an unprivileged user inside pinned
// distro root filesystems, entered through user namespaces by tests/rootless/enter. When the test
// binary runs as root (as on this host), every process of the matrix is started as the user
// `bobtest` with its credentials, so bob always runs without privileges.

const guestHome = "/home/bobtest"

var (
	setupOnce sync.Once
	setup     *world
	setupErr  error
)

// world is what all tests share: built binaries, the rootfs cache and the unprivileged identity.
type world struct {
	share        string // world-readable scratch root (0755); everything handed to enter lives here
	enter        string
	bob          string // static bob
	bobTest      string // bob built with -tags bobtest (fault hooks for R-NIX-022)
	cred         *syscall.Credential
	fetcher      *Fetcher
	rootfsDir    string
	counter      int
	counterMutex sync.Mutex
}

func mustWorld(t testing.TB) *world {
	t.Helper()
	setupOnce.Do(func() { setup, setupErr = newWorld(t) })
	if setupErr != nil {
		t.Fatalf("harness: %v", setupErr)
	}
	return setup
}

func newWorld(t testing.TB) (*world, error) {
	w := &world{}
	root := harness.Root(t)
	share, err := os.MkdirTemp("/var/tmp", "bob-rootless-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(share, 0o755); err != nil {
		return nil, err
	}
	w.share = share
	if os.Getuid() == 0 {
		u, err := user.Lookup("bobtest")
		if err != nil {
			return nil, fmt.Errorf("the rootless matrix needs the unprivileged user bobtest (HARNESS.md §7): %w", err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		w.cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	build := func(out string, tags string, pkg string) error {
		args := []string{"build", "-o", out}
		if tags != "" {
			args = append(args, "-tags", tags)
		}
		args = append(args, pkg)
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build %s: %w: %s", pkg, err, b)
		}
		return os.Chmod(out, 0o755)
	}
	w.enter, w.bob, w.bobTest = filepath.Join(share, "enter"), filepath.Join(share, "bob"), filepath.Join(share, "bob-test")
	if err := build(w.enter, "", "./tests/rootless/enter"); err != nil {
		return nil, err
	}
	if err := build(w.bob, "", "./cmd/bob"); err != nil {
		return nil, err
	}
	if err := build(w.bobTest, "bobtest", "./cmd/bob"); err != nil {
		return nil, err
	}
	cache := os.Getenv("BOB_ROOTFS_CACHE")
	if cache == "" {
		cache = "/var/tmp/bob-rootfs-cache"
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	w.rootfsDir = filepath.Join(cache, "rootfs")
	w.fetcher = &Fetcher{Blobs: filepath.Join(cache, "blobs")}
	if w.cred != nil {
		w.fetcher.ChownTo = &[2]int{int(w.cred.Uid), int(w.cred.Gid)}
	}
	return w, nil
}

// chown hands path (recursively) to the unprivileged user when running as root.
func (w *world) chown(path string) error {
	if w.cred == nil {
		return nil
	}
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(w.cred.Uid), int(w.cred.Gid))
	})
}

// dir makes a fresh 0755 directory below share, owned by the unprivileged user.
func (w *world) dir(t testing.TB, name string) string {
	t.Helper()
	w.counterMutex.Lock()
	w.counter++
	n := w.counter
	w.counterMutex.Unlock()
	d := filepath.Join(w.share, fmt.Sprintf("%s-%d", name, n))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.chown(d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// repo copies a fixture repository (git-initialised by the harness) below share.
func (w *world) repo(t testing.TB, fixture string) string {
	t.Helper()
	src := harness.Fixture(t, fixture)
	dst := w.dir(t, "repo-"+fixture)
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := w.chown(dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

// rootfs returns the extracted rootfs of im, fetching it on first use.
func (w *world) rootfs(t testing.TB, im Image) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d, err := w.fetcher.Rootfs(ctx, im, w.rootfsDir)
	if err != nil {
		t.Fatalf("harness: rootfs %s: %v", im.Name, err)
	}
	return d
}

// opts describes one command inside a rootfs.
type opts struct {
	Image       Image
	Bob         string   // binary to mount at /usr/local/bin/bob (default: the plain bob)
	Home        string   // host directory mounted as the guest's HOME (state lives in it)
	Work        string   // host directory mounted at /work (the cwd)
	Env         []string // added to PATH, HOME and TERM
	Decoy       string   // host directory with decoy `nix` binaries, first on PATH
	BlockUserns bool     // user.max_user_namespaces = 0 for the command
	NixOS       bool     // /etc/NIXOS exists
	Timeout     time.Duration
}

// run executes argv (inside the rootfs) as the unprivileged user.
func (w *world) run(t testing.TB, o opts, argv ...string) harness.Result {
	t.Helper()
	if o.Timeout == 0 {
		o.Timeout = 25 * time.Minute
	}
	scratch := w.dir(t, "scratch")
	args := []string{"-rootfs", w.rootfs(t, o.Image), "-scratch", scratch}
	if o.Work != "" {
		args = append(args, "-cwd", "/work")
	}
	bobBin := o.Bob
	if bobBin == "" {
		bobBin = w.bob
	}
	args = append(args, "-ro-bind", bobBin+":/usr/local/bin/bob")
	path := "/usr/bin:/bin"
	if o.Decoy != "" {
		args = append(args, "-ro-bind", o.Decoy+":/decoy")
		path = "/decoy:" + path
	}
	if o.Home != "" {
		args = append(args, "-bind", o.Home+":"+guestHome)
	}
	if o.Work != "" {
		args = append(args, "-bind", o.Work+":/work")
	}
	for _, e := range append([]string{"PATH=" + path, "HOME=" + guestHome, "TERM=dumb"}, o.Env...) {
		args = append(args, "-env", e)
	}
	if o.BlockUserns {
		args = append(args, "-block-userns")
	}
	if o.NixOS {
		args = append(args, "-nixos")
	}
	args = append(args, "--")
	args = append(args, argv...)

	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.enter, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if w.cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: w.cred}
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	res := harness.Result{Args: argv}
	err := cmd.Run()
	res.Stdout, res.Stderr = so.String(), se.String()
	if ee, ok := err.(*exec.ExitError); ok {
		res.Exit = ee.ExitCode()
	} else if err != nil {
		res.Exit = -1
		res.Stderr += "\nharness: " + err.Error()
	}
	if res.Exit == 120 {
		t.Fatalf("harness: enter failed (image %s): %s", o.Image.Name, strings.TrimSpace(res.Stderr))
	}
	return res
}

// guestBob is the path of bob inside the rootfs.
const guestBob = "/usr/local/bin/bob"

// checkNamespaceHonest verifies the simulation itself: with -block-userns the command cannot
// create a user namespace (ENOSPC), and without it can.
func (w *world) checkNamespaceHonest(t testing.TB, im Image, blocked bool) {
	t.Helper()
	r := w.run(t, opts{Image: im, BlockUserns: blocked, Timeout: 2 * time.Minute}, "@probe-userns")
	want := "userns: ok"
	if blocked {
		want = "userns: ENOSPC"
	}
	if !strings.Contains(r.Stdout, want) {
		t.Fatalf("harness: the simulation is wrong in %s (blocked=%v): got %q, want %q", im.Name, blocked, strings.TrimSpace(r.Stdout+r.Stderr), want)
	}
}

// TestMain removes the shared scratch root (built binaries and per-test directories) when the
// run ends; the rootfs cache outside it is kept.
func TestMain(m *testing.M) {
	m.Run() // the test wrapper passes its result to os.Exit when TestMain returns (Go ≥ 1.15)
	if setup != nil {
		_ = os.RemoveAll(setup.share)
	}
}
