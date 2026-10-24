package evalbox

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Regression tests of review finding F19 / A94: when bob runs as root the evaluator sandbox
// runs as nobody (65534), both for host root and for root inside bob's own namespace that host
// root entered (store.Enter maps 65534 next to 0); a namespace whose uid 0 is host root without
// nobody refuses to evaluate instead of running as root.

const (
	helperEnv = "BOB_EVALBOX_TEST_HELPER" // sandbox step: write /out/id
	childEnv  = "BOB_EVALBOX_TEST_CHILD"  // re-executed test in a user namespace: run the sandbox
)

// TestHelperWriteID is the step inside the sandbox (only with helperEnv set).
func TestHelperWriteID(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("helper")
	}
	if err := os.WriteFile("/out/id", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runAsSandbox runs TestHelperWriteID in a sandbox and returns the host owner of the file it
// wrote. dir holds a copy of the test binary (bin) and the writable out directory.
func runAsSandbox(dir string) (uint32, error) {
	out := filepath.Join(dir, "out")
	_, err := Run(context.Background(), Spec{
		Binds: []Bind{
			{Host: filepath.Join(dir, "bin"), Sandbox: "/t", Exec: true},
			{Host: out, Sandbox: "/out", Writable: true},
		},
		Dir:    "/",
		Steps:  []Step{{Args: []string{"/t/evalbox.test", "-test.run=^TestHelperWriteID$"}, Env: []string{helperEnv + "=1"}}},
		Limits: Limits{WallClock: time.Minute},
	})
	if err != nil {
		return 0, err
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(out, "id"), &st); err != nil {
		return 0, err
	}
	return st.Uid, nil
}

// TestChildRunSandbox runs the sandbox from inside the user namespace the parent test created
// (only with childEnv set) and reports the result on stdout.
func TestChildRunSandbox(t *testing.T) {
	dir := os.Getenv(childEnv)
	if dir == "" {
		t.Skip("child")
	}
	uid, err := runAsSandbox(dir)
	if err != nil {
		os.Stdout.WriteString("RESULT error " + strings.ReplaceAll(err.Error(), "\n", " ") + "\n")
		return
	}
	os.Stdout.WriteString("RESULT ok\n")
	_ = uid
}

func sandboxDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{dir, filepath.Join(dir, "bin"), filepath.Join(dir, "out")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, "bin", "evalbox.test"), os.O_WRONLY|os.O_CREATE, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func needHostRoot(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the sandbox")
	}
	if os.Getuid() != 0 || !mapsToParent("/proc/self/uid_map", 0, 0) || !idMapped("/proc/self/uid_map", nobody) {
		t.Skip("needs host root")
	}
}

func TestHostRootRunsAsNobody(t *testing.T) {
	needHostRoot(t)
	uid, err := runAsSandbox(sandboxDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if uid != nobody {
		t.Errorf("the sandbox wrote as host uid %d, want %d", uid, nobody)
	}
}

// TestRootNamespaceRunsAsNobody enters a user namespace the way store.Enter does for host root
// (0 → 0 and 65534 → 65534) and runs the sandbox there: it must still write as nobody. Without
// the nobody mapping (0 → 0 only) the sandbox refuses instead of running as root.
func TestRootNamespaceRunsAsNobody(t *testing.T) {
	needHostRoot(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, withNobody := range map[string]bool{"with nobody": true, "without nobody": false} {
		t.Run(name, func(t *testing.T) {
			dir := sandboxDir(t)
			cmd := exec.Command(exe, "-test.run=^TestChildRunSandbox$", "-test.v")
			cmd.Env = append(os.Environ(), childEnv+"="+dir)
			uids := []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}}
			gids := []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}}
			if withNobody {
				uids = append(uids, syscall.SysProcIDMap{ContainerID: nobody, HostID: nobody, Size: 1})
				gids = append(gids, syscall.SysProcIDMap{ContainerID: nobody, HostID: nobody, Size: 1})
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
				UidMappings: uids, GidMappings: gids, GidMappingsEnableSetgroups: false}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			text := string(out)
			if !withNobody {
				if !strings.Contains(text, "RESULT error") || !strings.Contains(text, "refusing to evaluate as root") {
					t.Errorf("without nobody the sandbox did not refuse:\n%s", text)
				}
				return
			}
			if !strings.Contains(text, "RESULT ok") {
				t.Fatalf("child:\n%s", text)
			}
			var st syscall.Stat_t
			if err := syscall.Lstat(filepath.Join(dir, "out", "id"), &st); err != nil {
				t.Fatal(err)
			}
			if st.Uid != nobody {
				t.Errorf("the sandbox wrote as host uid %d, want %d", st.Uid, nobody)
			}
		})
	}
}
