//go:build slow

package outputs

// HARNESS.md §7.3 suite bundles-distro: the fixture bundles run in unprivileged user namespaces
// (tests/rootless/enter, the rootless matrix's sandbox; it also provides -block-userns, which
// stands for `bwrap --disable-userns`) on the 5 pinned rootfs images × {userns, nouserns}, arm64
// native. Run: go test -tags slow -run Distro ./tests/outputs/ -timeout 3h
// amd64 rows (native worker, or qemu-user where possible) and real AppArmor hosts are (manual)
// on this host, HARNESS.md §7.2/§16.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/rootless"
)

// guest runs argv in a rootfs image as the unprivileged user.
type guest struct {
	t      *testing.T
	enter  string
	share  string
	rootfs string
	cred   *syscall.Credential
}

func newGuest(t *testing.T, im rootless.Image) *guest {
	t.Helper()
	g := &guest{t: t}
	root := harness.Root(t)
	share, err := os.MkdirTemp("/var/tmp", "bob-bundles-")
	if err != nil || os.Chmod(share, 0o755) != nil {
		t.Fatalf("harness: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(share) })
	g.share = share
	var chown *[2]int
	if os.Getuid() == 0 {
		u, err := user.Lookup("bobtest")
		if err != nil {
			t.Fatalf("harness: bobtest: %v", err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		g.cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
		chown = &[2]int{uid, gid}
	}
	g.enter = filepath.Join(share, "enter")
	cmd := exec.Command("go", "build", "-o", g.enter, "./tests/rootless/enter")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: build enter: %v: %s", err, b)
	}
	cache := os.Getenv("BOB_ROOTFS_CACHE")
	if cache == "" {
		cache = "/var/tmp/bob-rootfs-cache"
	}
	f := &rootless.Fetcher{Blobs: filepath.Join(cache, "blobs"), ChownTo: chown}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if g.rootfs, err = f.Rootfs(ctx, im, filepath.Join(cache, "rootfs")); err != nil {
		t.Fatalf("harness: rootfs %s: %v", im.Name, err)
	}
	return g
}

// file copies a host file into the share (readable by the guest user) and returns the path.
func (g *guest) file(src string) string {
	g.t.Helper()
	dst := filepath.Join(g.share, filepath.Base(src))
	if err := os.WriteFile(dst, readFile(g.t, src), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if g.cred != nil {
		_ = os.Chown(dst, int(g.cred.Uid), int(g.cred.Gid))
	}
	return dst
}

// run executes argv in the guest; binds are host:guest read-only pairs.
func (g *guest) run(blockUserns bool, env []string, binds map[string]string, argv ...string) harness.Result {
	g.t.Helper()
	scratch, err := os.MkdirTemp(g.share, "scratch-")
	if err != nil {
		g.t.Fatal(err)
	}
	home, _ := os.MkdirTemp(g.share, "home-")
	for _, d := range []string{scratch, home} {
		_ = os.Chmod(d, 0o755)
		if g.cred != nil {
			_ = os.Chown(d, int(g.cred.Uid), int(g.cred.Gid))
		}
	}
	args := []string{"-rootfs", g.rootfs, "-scratch", scratch, "-bind", home + ":/home/bobtest"}
	for h, gp := range binds {
		args = append(args, "-ro-bind", h+":"+gp)
	}
	for _, e := range append([]string{"PATH=/usr/bin:/bin", "HOME=/home/bobtest", "TERM=dumb"}, env...) {
		args = append(args, "-env", e)
	}
	if blockUserns {
		args = append(args, "-block-userns")
	}
	args = append(args, "--")
	args = append(args, argv...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.enter, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if g.cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: g.cred}
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r := harness.Result{Args: argv}
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			r.Exit = ee.ExitCode()
		} else {
			r.Exit = -1
		}
	}
	r.Stdout, r.Stderr = so.String(), se.String()
	if r.Exit == 120 {
		g.t.Fatalf("harness: enter failed (%s): %s", filepath.Base(g.rootfs), strings.TrimSpace(r.Stderr))
	}
	return r
}

// R-OUT-003, R-OUT-017, R-OUT-018: the tools AppImage (api, curl, date, iconv) on every rootfs
// image, with and without user namespaces: golden line, HTTPS 200, no locale warnings with
// LANG=en_US.UTF-8, date, and iconv ä → ISO-8859-15 = e4.
func TestDistroAppImageMatrix(t *testing.T) {
	parity.Covers(t, "R-OUT-003", "R-OUT-017", "R-OUT-018")
	w := appWorld(t)
	file, ok := w.appImage("//apptest:tools")
	if !ok {
		return
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	cert := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, im := range rootless.Images {
		t.Run(im.Name, func(t *testing.T) {
			g := newGuest(t, im)
			app := "/work/" + filepath.Base(file)
			binds := map[string]string{g.file(file): app, cert: "/work/cert.pem"}
			for _, row := range []struct {
				name  string
				block bool
				modes []string
			}{{"userns", false, []string{"userns", "ldso", "auto"}}, {"nouserns", true, []string{"auto", "ldso"}}} {
				for _, mode := range row.modes {
					env := []string{"BOB_APPIMAGE_MODE=" + mode, "LANG=en_US.UTF-8"}
					tag := row.name + "/" + mode
					if r := g.run(row.block, env, binds, app); r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
						t.Errorf("%s: api exit %d, output %q%s", tag, r.Exit, r.Stdout, r.Stderr)
					}
					r := g.run(row.block, append(env, "BOB_ENTRY=curl"), binds, app, "-sS", "--cacert", "/work/cert.pem", "-o", "/dev/null", "-w", "%{http_code}", srv.URL)
					if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "200" {
						t.Errorf("%s: HTTPS GET exit %d, status %q\n%s", tag, r.Exit, r.Stdout, r.Stderr)
					}
					r = g.run(row.block, append(env, "BOB_ENTRY=date"), binds, app, "-u", "-d", "@0")
					// en_US.UTF-8 (bundled) formats the time with %r: "12:00:00 AM" (glibc 2.42
					// date_fmt "%a %b %e %r %Z %Y"); the C form appears only without the locale
					// (test correction, I-M12b).
					if got := strings.TrimSpace(r.Stdout); r.Exit != 0 || got != "Thu Jan  1 00:00:00 UTC 1970" && got != "Thu Jan  1 12:00:00 AM UTC 1970" || r.Stderr != "" {
						t.Errorf("%s: date printed %q (exit %d) stderr %q, want the epoch and no locale warnings", tag, got, r.Exit, r.Stderr)
					}
					r = g.run(row.block, append(env, "BOB_ENTRY=iconv"), binds, "/bin/sh", "-c",
						"printf '\\303\\244' | "+app+" -f UTF-8 -t ISO-8859-15 | od -An -tx1")
					if got := strings.TrimSpace(r.Stdout); r.Exit != 0 || got != "e4" {
						t.Errorf("%s: iconv ä → ISO-8859-15 printed %q (exit %d)\n%s", tag, got, r.Exit, r.Stderr)
					}
				}
			}
		})
	}
}

// R-OUT-020: with HostGraphics the bundled vulkaninfo finds the host's Mesa (llvmpipe) in both
// modes. The pinned nixpkgs mesa stands in for the Fedora mesa-vulkan-drivers package: its
// lib directory is mounted at /usr/lib64 together with the host store it links against. Real
// GPUs are (manual). Needs the pin "mesa".
func TestDistroHostGraphics(t *testing.T) {
	parity.Covers(t, "R-OUT-020")
	w := appWorld(t)
	file, ok := w.appImage("//apptest:graphics")
	if !ok {
		return
	}
	mesa := pinned(t, "mesa")
	var fedora rootless.Image
	for _, im := range rootless.Images {
		if im.OSID == "fedora" {
			fedora = im
		}
	}
	g := newGuest(t, fedora)
	app := "/work/" + filepath.Base(file)
	binds := map[string]string{g.file(file): app, filepath.Join(mesa, "lib"): "/usr/lib64", "/nix/store": "/nix/store"}
	icd, _ := filepath.Glob(filepath.Join(mesa, "share", "vulkan", "icd.d", "lvp_icd*.json"))
	if len(icd) == 0 {
		t.Fatalf("harness: the mesa pin has no lavapipe ICD file")
	}
	for _, mode := range []string{"userns", "ldso"} {
		r := g.run(false, []string{"BOB_APPIMAGE_MODE=" + mode, "VK_ICD_FILENAMES=" + icd[0]}, binds, app, "--summary")
		if r.Exit != 0 || !strings.Contains(r.Stdout, "llvmpipe") {
			t.Errorf("mode %s: vulkaninfo exit %d, no llvmpipe in:\n%.800s%s", mode, r.Exit, r.Stdout, r.Stderr)
		}
	}
}
