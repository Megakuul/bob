package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestHostStoreView (A551) runs this binary inside bob's namespace on a host with a Nix store
// (the NixOS case of the owner's `bob dev` failure): the namespace itself must not show the
// host's store (builds stay hermetic), StartHost must show it below bob's store, programs that
// are links into it must start, and a missing program must be reported with its errno.
func TestHostStoreView(t *testing.T) {
	if os.Getenv("BOB_TEST_HOSTVIEW_CHILD") != "" {
		t.Skip("child")
	}
	entries, err := os.ReadDir("/nix/store")
	if err != nil || len(entries) == 0 {
		t.Skip("the host has no Nix store")
	}
	home := filepath.Join(t.TempDir(), "bobhome")
	if _, err := Open(context.Background(), Config{Home: home}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "nix", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "nix", "store", "bobtest-only-in-bob"), []byte("bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A shell that is a link into the host store, reached through a link outside it (the
	// layout of /run/current-system/sw/bin/fish).
	shells, _ := filepath.Glob("/nix/store/*-bash-*/bin/bash")
	sys := filepath.Join(t.TempDir(), "sys")
	if len(shells) > 0 {
		if err := os.MkdirAll(filepath.Join(sys, "sw", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(shells[0], filepath.Join(sys, "sw", "bin", "fish")); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BOB_TEST_HOSTVIEW_CHILD", entries[0].Name())
	t.Setenv("BOB_TEST_HOSTUID", strconv.Itoa(os.Getuid()))
	t.Setenv("BOB_TEST_HOSTGID", strconv.Itoa(os.Getgid()))
	t.Setenv("BOB_TEST_HOSTVIEW_SHELL", filepath.Join(sys, "sw", "bin", "fish"))
	t.Setenv("BOB_HOME", home)
	saved := os.Args
	os.Args = []string{saved[0], "-test.run=^TestHostStoreViewChild$", "-test.count=1", "-test.v"}
	inside, exit, err := Enter(context.Background(), Config{Home: home})
	os.Args = saved
	if err != nil || inside || exit != 0 {
		t.Fatalf("the child failed: Enter = %v, %d, %v (see its output above)", inside, exit, err)
	}
	if fi, err := os.Stat(filepath.Join(home, "made-by-session")); err != nil {
		t.Errorf("the session's file: %v", err)
	} else if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		t.Errorf("a file made in the dev session belongs to uid %d, want %d", st.Uid, os.Getuid())
	}
}

// TestExecRunsAsUser (A561): programs of bob exec (the shims of the dev tools) run as the user,
// with the user's name from /etc/passwd.
func TestExecRunsAsUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: the namespace's root is the user")
	}
	home := filepath.Join(t.TempDir(), "bobhome")
	if _, err := Open(context.Background(), Config{Home: home}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOB_HOME", home)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	c := &exec.Cmd{Path: sh, Args: []string{"sh", "-c", "id -u; id -g; id -un; touch " + filepath.Join(home, "by-exec")}, Stdout: &b, Stderr: os.Stderr}
	if err := StartIn(home, c); err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(); err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(os.Getuid()) + "\n" + strconv.Itoa(os.Getgid()) + "\n"; !strings.HasPrefix(b.String(), want) {
		t.Errorf("bob exec runs as %q, want uid/gid %q", b.String(), want)
	}
	if u, err := user.Current(); err == nil && !strings.HasSuffix(strings.TrimSpace(b.String()), u.Username) {
		t.Errorf("bob exec's user name %q, want %s", b.String(), u.Username)
	}
	if fi, err := os.Stat(filepath.Join(home, "by-exec")); err != nil || fi.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Errorf("file made by bob exec: %v, %v", fi, err)
	}
}

func TestHostStoreViewChild(t *testing.T) {
	entry := os.Getenv("BOB_TEST_HOSTVIEW_CHILD")
	if entry == "" {
		t.Skip("run by TestHostStoreView")
	}
	home, err := HomeDir("")
	if err != nil {
		t.Fatal(err)
	}
	if !Inside(home) || !HostViewAvailable(home) {
		t.Fatalf("inside = %v, host view available = %v", Inside(home), HostViewAvailable(home))
	}
	// The namespace of the command: no host store (a build must not see it).
	if _, err := os.Lstat("/nix/store/" + entry); err == nil {
		t.Errorf("/nix/store/%s is visible in the namespace of builds", entry)
	}
	run := func(argv ...string) error {
		c := &exec.Cmd{Path: argv[0], Args: argv[1:], Stdout: os.Stdout, Stderr: os.Stderr}
		if err := StartHost("", c); err != nil {
			return err
		}
		return c.Wait()
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	// The dev session: the host store below bob's.
	if err := run(sh, "sh", "-c", "test -e /nix/store/"+entry+" && test -e /nix/store/bobtest-only-in-bob"); err != nil {
		t.Errorf("the host store view does not show both stores: %v", err)
	}
	if shell := os.Getenv("BOB_TEST_HOSTVIEW_SHELL"); shell != "" {
		if _, err := os.Stat(shell); err == nil {
			t.Errorf("%s resolves in the namespace of builds", shell)
		}
		if err := run(shell, shell, "-c", "exit 0"); err != nil {
			t.Errorf("a shell that links into the host store does not start: %v", err)
		}
	}
	// A561: the session runs as the user, not as the namespace's root, in both the view and
	// the fallback without it; the namespace of the command itself keeps the root mapping.
	wantUID, wantGID := os.Getenv("BOB_TEST_HOSTUID"), os.Getenv("BOB_TEST_HOSTGID")
	if got := output(t, run, sh, "sh", "-c", "id -u; id -g; echo $HOME"); !strings.HasPrefix(got, wantUID+"\n"+wantGID+"\n") {
		t.Errorf("the dev session runs as %q, want uid %s gid %s", strings.Fields(got), wantUID, wantGID)
	}
	if os.Getuid() != 0 {
		t.Errorf("the namespace of the command is not the root mapping: uid %d", os.Getuid())
	}
	// A file created in the session belongs to the user on the host (checked by the outer test).
	if err := run(sh, "sh", "-c", "touch "+filepath.Join(os.Getenv("BOB_HOME"), "made-by-session")); err != nil {
		t.Errorf("a session cannot create a file in the state home: %v", err)
	}
	// The namespace of builds is unchanged by a dev session.
	if _, err := os.Lstat("/nix/store/" + entry); err == nil {
		t.Errorf("/nix/store/%s is visible after StartHost", entry)
	}
	// A bob started in the dev session is not in the namespace of builds: it enters its own.
	self, _ := os.Executable()
	t.Setenv("BOB_TEST_HOSTVIEW_PROBE", "1")
	if err := run(self, self, "-test.run=^TestHostStoreViewProbe$", "-test.count=1"); err != nil {
		t.Errorf("a process of the dev session counts as inside the namespace of builds: %v", err)
	}
	err = run("/nonexistent/fish", "/nonexistent/fish")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing program: %v, want a not-exist error", err)
	}
	if err := run(sh, "sh", "-c", "exit 3"); err == nil {
		t.Error("the exit status of the program is lost")
	}
}

// output runs argv through run (StartHost) with its standard output captured.
func output(t *testing.T, _ func(...string) error, path string, argv ...string) string {
	t.Helper()
	var b strings.Builder
	c := &exec.Cmd{Path: path, Args: argv, Stdout: &b, Stderr: os.Stderr}
	if err := StartHost("", c); err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestHostStoreViewProbe(t *testing.T) {
	if os.Getenv("BOB_TEST_HOSTVIEW_PROBE") == "" {
		t.Skip("run by TestHostStoreViewChild")
	}
	home, err := HomeDir("")
	if err != nil {
		t.Fatal(err)
	}
	if inNamespace(home) {
		t.Fatal("inNamespace is true below the host store overlay")
	}
}
