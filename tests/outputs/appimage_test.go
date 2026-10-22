package outputs

import (
	"debug/buildinfo"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

func appWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, "polyglot")
	w.overlay(appOverlay())
	return w
}

// appImage builds label and returns the AppImage file of its output.
func (w *world) appImage(label string, extra ...string) (string, bool) {
	w.t.Helper()
	out, ok := w.buildOne(label, extra...)
	if !ok {
		return "", false
	}
	return onlyFile(w.t, out, "*.AppImage"), true
}

// runApp runs an AppImage with a private cache and home; env entries are added.
func runApp(t testing.TB, file string, env []string, args ...string) harness.Result {
	t.Helper()
	home := t.TempDir()
	return harness.RunPath(t, file, harness.Spec{
		Env:     append([]string{"HOME=" + home, "XDG_CACHE_HOME=" + filepath.Join(home, "cache")}, env...),
		Timeout: 2 * time.Minute,
	}, args...)
}

// extractApp runs --appimage-extract in a fresh directory and returns <dir>/squashfs-root.
func extractApp(t testing.TB, file string, glob ...string) string {
	t.Helper()
	dir := t.TempDir()
	r := harness.RunPath(t, file, harness.Spec{Dir: dir, Env: []string{"HOME=" + dir}}, append([]string{"--appimage-extract"}, glob...)...)
	if r.Exit != 0 {
		t.Fatalf("%s --appimage-extract: exit %d\n%s", filepath.Base(file), r.Exit, r.Combined())
	}
	return filepath.Join(dir, "squashfs-root")
}

// licence is one entry of licences.json (R-OUT-008).
type licence struct {
	Path     string   `json:"path"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Licenses []string `json:"licenses"`
}

func (l licence) has(id string) bool {
	for _, x := range l.Licenses {
		if x == id {
			return true
		}
	}
	return false
}

// checkLicences checks the licences.json rules of R-OUT-008 and the glibc and openssl entries.
func checkLicences(t testing.TB, what string, raw []byte) {
	t.Helper()
	ls := decode[[]licence](t, raw)
	if len(ls) == 0 {
		t.Errorf("%s: licences.json is empty", what)
	}
	var paths []string
	found := map[string]bool{}
	for _, l := range ls {
		paths = append(paths, l.Path)
		if !strings.HasPrefix(l.Path, "/nix/store/") || l.Name == "" || len(l.Licenses) == 0 {
			t.Errorf("%s: malformed licence entry %+v", what, l)
		}
		switch l.Name {
		case "glibc":
			found["glibc"] = l.has("LGPL-2.1-or-later")
		case "openssl":
			found["openssl"] = l.has("Apache-2.0")
		}
	}
	for _, p := range []string{"glibc", "openssl"} {
		if !found[p] {
			t.Errorf("%s: licences.json does not list %s with its licence (glibc LGPL-2.1-or-later, openssl Apache-2.0)", what, p)
		}
	}
	sorted := append([]string(nil), paths...)
	sortStrings(sorted)
	if strings.Join(sorted, "\n") != strings.Join(paths, "\n") {
		t.Errorf("%s: licences.json entries are not sorted by path", what)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// R-OUT-007: the api AppImage with DefaultPrune is ≤ 12 MB and runs; pruning a library the
// binary needs is a build error naming the pattern and the library (also for images).
func TestROUT007Pruning(t *testing.T) {
	parity.Covers(t, "R-OUT-007")
	w := appWorld(t)
	file, ok := w.appImage("//deploy:api-appimage")
	if !ok {
		return
	}
	if n := fileSize(t, file); n > 12_000_000 {
		t.Errorf("api AppImage is %d bytes, want ≤ 12 MB with DefaultPrune", n)
	}
	if r := runApp(t, file, nil); r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
		t.Errorf("pruned AppImage printed (exit %d) %q%s, want the golden line", r.Exit, r.Stdout, r.Stderr)
	}
	const pattern = "/nix/store/*-openssl-*/lib/libssl.so*"
	for label, name := range map[string]string{"//apptest:bundle": "bundle", "//ocitest:pruned": "pruned"} {
		r := w.bob("build", label)
		if harness.Red(t, r) {
			return
		}
		want := "✗ " + label + ": prune pattern \"" + pattern + "\" removes libssl.so.3, which api needs"
		if r.Exit != 1 || !strings.Contains(r.Stderr, want) {
			t.Errorf("bob build %s (%s): exit %d, want 1 and %q\n%s", label, name, r.Exit, want, r.Stderr)
		}
	}
}

// R-OUT-008: every bundle carries licences.json (AppImage .bob/licences.json, tarball
// bob/licences.json, OCI label and /.bob/licences.json).
func TestROUT008LicenceManifest(t *testing.T) {
	parity.Covers(t, "R-OUT-008")
	w := newWorld(t, "polyglot")
	paths, ok := w.build("//deploy:api-appimage", "//deploy:api-tar", "//deploy:api-image")
	if !ok {
		return
	}
	app := onlyFile(t, paths["//deploy:api-appimage"], "*.AppImage")
	root := extractApp(t, app, ".bob/licences.json")
	checkLicences(t, "AppImage", readFile(t, filepath.Join(root, ".bob", "licences.json")))

	tgz := onlyFile(t, paths["//deploy:api-tar"], "*.tar.gz")
	es := readTar(t, readFile(t, tgz))
	if e, ok := tarFind(es, "bob/licences.json"); !ok {
		t.Errorf("tarball has no bob/licences.json")
	} else {
		checkLicences(t, "tarball", e.Data)
	}

	l := openLayout(t, paths["//deploy:api-image"])
	m, _, cfg := l.manifestFor(ociArch(hostPlatform(t)))
	if cfg.Config.Labels["org.opencontainers.image.licenses"] == "" {
		t.Errorf("image config has no org.opencontainers.image.licenses label")
	}
	if e, ok := tarFind(l.layerEntries(m, len(m.Layers)-1), ".bob/licences.json"); !ok {
		t.Errorf("customisation layer has no /.bob/licences.json")
	} else {
		checkLicences(t, "image", e.Data)
	}
}

// R-OUT-015: file format, runtime, extraction cache and flags.
func TestROUT015FileFormatAndRuntime(t *testing.T) {
	parity.Covers(t, "R-OUT-015")
	w := newWorld(t, "polyglot")
	file, ok := w.appImage("//deploy:api-appimage")
	if !ok {
		return
	}
	other, ok := w.freshStore().appImage("//deploy:api-appimage")
	if !ok {
		return
	}
	sum := sha256File(t, file)
	if sum != sha256File(t, other) {
		t.Errorf("two fresh builds give different AppImages")
	}
	golden.Assert(t, "tests/goldens/outputs/appimage.sha256", []byte(sum+"\n"), golden.Reqs("R-OUT-015"))

	b := readFile(t, file)
	if string(b[8:11]) != "AI\x02" {
		t.Errorf("bytes 8-10 are %q, want AI\\x02", b[8:11])
	}
	if got := elfMachine(t, file).String(); got != map[string]string{"linux-arm64": "EM_AARCH64", "linux-amd64": "EM_X86_64"}[string(hostPlatform(t))] {
		t.Errorf("runtime ELF machine %s", got)
	}
	bi, err := buildinfo.ReadFile(file)
	if err != nil {
		t.Errorf("the runtime is not a Go binary: %v", err)
	} else {
		for _, d := range bi.Deps {
			if !strings.HasPrefix(d.Path, "golang.org/x/") {
				t.Errorf("runtime depends on module %s; only BSD-licensed golang.org/x modules are allowed (no LGPL/GPL)", d.Path)
			}
		}
		if bi.Settings != nil {
			for _, s := range bi.Settings {
				if s.Key == "CGO_ENABLED" && s.Value != "0" {
					t.Errorf("runtime built with CGO_ENABLED=%s", s.Value)
				}
			}
		}
	}

	r := runApp(t, file, nil, "--appimage-offset")
	off, err := strconv.ParseInt(strings.TrimSpace(r.Stdout), 10, 64)
	if r.Exit != 0 || err != nil {
		t.Fatalf("--appimage-offset: exit %d, output %q", r.Exit, r.Stdout)
	}
	sb := b[off : off+96]
	if string(sb[:4]) != "hsqs" {
		t.Errorf("no squashfs magic at offset %d", off)
	}
	if mk := binary.LittleEndian.Uint32(sb[8:]); mk != 1 {
		t.Errorf("squashfs mkfs_time %d, want 1", mk)
	}
	if bs := binary.LittleEndian.Uint32(sb[12:]); bs != 1<<20 {
		t.Errorf("squashfs block size %d, want 1 MiB", bs)
	}
	if c := binary.LittleEndian.Uint16(sb[20:]); c != 6 {
		t.Errorf("squashfs compression id %d, want 6 (zstd)", c)
	}
	un := run(t, 2*time.Minute, []string{"PATH=/usr/bin:/bin"}, pinnedBin(t, "squashfs-tools", "unsquashfs"), "-o", strconv.FormatInt(off, 10), "-l", file)
	if un.Exit != 0 || !strings.Contains(un.Stdout, "squashfs-root/AppRun") {
		t.Errorf("unsquashfs -o %d -l: exit %d, no AppRun in the listing\n%.500s", off, un.Exit, un.Combined())
	}
	for _, f := range []string{"--appimage-version", "--appimage-help"} {
		if r := runApp(t, file, nil, f); r.Exit != 0 || strings.TrimSpace(r.Stdout+r.Stderr) == "" {
			t.Errorf("%s: exit %d, output %q", f, r.Exit, r.Stdout+r.Stderr)
		}
	}

	// AppDir layout and launch.json.
	root := extractApp(t, file)
	for _, f := range []string{"AppRun", ".DirIcon", ".bob/launch.json", ".bob/licences.json", "nix/store"} {
		if _, err := os.Lstat(filepath.Join(root, f)); err != nil {
			t.Errorf("AppDir has no %s", f)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(root, "*.desktop")); len(m) != 1 {
		t.Errorf("AppDir has %d .desktop files, want 1", len(m))
	}
	if m1, _ := filepath.Glob(filepath.Join(root, "*.svg")); len(m1) == 0 {
		if m2, _ := filepath.Glob(filepath.Join(root, "*.png")); len(m2) == 0 {
			t.Errorf("AppDir has no icon (<name>.svg or .png)")
		}
	}
	lj := decode[map[string]any](t, readFile(t, filepath.Join(root, ".bob", "launch.json")))
	if lj["schema"] != "bob.appimage.launch/v1" || lj["mode"] != "auto" || lj["hostGraphics"] != false {
		t.Errorf("launch.json header: %v", lj)
	}
	for _, k := range []string{"entry", "interp", "libDirs", "env"} {
		if lj[k] == nil {
			t.Errorf("launch.json has no %s", k)
		}
	}

	// Extraction cache: the first runs extract once into $XDG_CACHE_HOME/bob/appimage/<sha256>/,
	// concurrent first runs leave one directory (start-up times are budget B-086).
	cache := t.TempDir()
	env := []string{"HOME=" + cache, "XDG_CACHE_HOME=" + cache}
	var wg sync.WaitGroup
	results := make([]harness.Result, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = harness.RunPath(t, file, harness.Spec{Env: env, Timeout: 2 * time.Minute})
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
			t.Errorf("concurrent first run %d: exit %d, output %q%s", i, r.Exit, r.Stdout, r.Stderr)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(cache, "bob", "appimage"))
	if len(ents) != 1 || ents[0].Name() != sum {
		t.Errorf("cache holds %v, want exactly one directory named %s", ents, sum)
	} else if _, err := os.Stat(filepath.Join(cache, "bob", "appimage", sum, ".complete")); err != nil {
		t.Errorf("cache directory has no .complete marker")
	}
}

// userNamespacesBlocked runs argv with user.max_user_namespaces = 0 inside a throwaway user
// namespace (per-namespace setting; the host is untouched, HARNESS.md §7.1).
func userNamespacesBlocked(t testing.TB, env []string, argv ...string) harness.Result {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	script := `echo 0 > /proc/sys/user/max_user_namespaces || exit 120; exec "$@"`
	return harness.RunPath(t, unshare, harness.Spec{Env: env, Timeout: 2 * time.Minute},
		append([]string{"--user", "--map-root-user", "--", "/bin/sh", "-c", script, "sh"}, argv...)...)
}

// R-OUT-016: launcher modes, debug output, error codes and signal forwarding.
func TestROUT016LauncherModes(t *testing.T) {
	parity.Covers(t, "R-OUT-016")
	w := appWorld(t)
	file, ok := w.appImage("//apptest:tools")
	if !ok {
		return
	}
	for _, mode := range []string{"userns", "ldso", "auto"} {
		r := runApp(t, file, []string{"BOB_APPIMAGE_MODE=" + mode, "BOB_APPIMAGE_DEBUG=1"})
		if r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
			t.Errorf("mode %s: exit %d, output %q%s", mode, r.Exit, r.Stdout, r.Stderr)
		}
		if !strings.Contains(r.Stderr, mode) && mode != "auto" {
			t.Errorf("mode %s: BOB_APPIMAGE_DEBUG=1 did not print the choice; stderr %q", mode, r.Stderr)
		}
	}
	// userns where namespaces are impossible: exit 127 with the launcher message; auto falls back.
	home := t.TempDir()
	env := []string{"HOME=" + home, "XDG_CACHE_HOME=" + home, "BOB_APPIMAGE_MODE=userns"}
	if r := userNamespacesBlocked(t, env, file); r.Exit != 127 || !strings.Contains(r.Stderr, "bobrun: user namespace:") {
		t.Errorf("userns mode without namespaces: exit %d, stderr %q, want 127 and `bobrun: user namespace:`", r.Exit, r.Stderr)
	}
	env[2] = "BOB_APPIMAGE_MODE=auto"
	if r := userNamespacesBlocked(t, env, file); r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
		t.Errorf("auto mode without namespaces: exit %d, output %q%s, want ldso fallback and the golden line", r.Exit, r.Stdout, r.Stderr)
	}
	if r := runApp(t, file, []string{"BOB_ENTRY=nope"}); r.Exit != 127 || !strings.Contains(r.Stderr, `bobrun: unknown entry "nope"`) {
		t.Errorf("unknown entry: exit %d, stderr %q", r.Exit, r.Stderr)
	}

	// kill -TERM of the AppImage process reaches the child: exit 143 in both modes.
	for _, mode := range []string{"userns", "ldso"} {
		h := t.TempDir()
		cmd := exec.Command(file, "60")
		cmd.Env = []string{"HOME=" + h, "XDG_CACHE_HOME=" + h, "BOB_ENTRY=sleep", "BOB_APPIMAGE_MODE=" + mode}
		if err := cmd.Start(); err != nil {
			t.Fatalf("harness: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		time.Sleep(3 * time.Second)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			code := -1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err == nil {
				code = 0
			}
			if code != 143 {
				t.Errorf("mode %s: after SIGTERM the AppImage exited %d, want 143", mode, code)
			}
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("mode %s: the AppImage did not exit 20 s after SIGTERM", mode)
		}
	}
}

// R-OUT-019: a shell-script entry in AutoMode warns at build time; UsernsOnly says it
// explicitly and the launcher refuses to run in ldso-only hosts.
func TestROUT019FidelityWarnings(t *testing.T) {
	parity.Covers(t, "R-OUT-019")
	w := appWorld(t)
	r := w.bob("build", "//apptest:script")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	want := regexp.MustCompile(`warning: //apptest:script: entry "deploy" is a script; it cannot run on hosts without user namespaces\. Set Mode: appimage\.UsernsOnly to make this explicit\.`)
	if !want.MatchString(r.Combined()) {
		t.Errorf("no script warning in the output:\n%s", r.Combined())
	}
	r = w.bob("build", "--print-paths", "--json", "//apptest:script-userns")
	if !exitIs(t, r, 0) {
		return
	}
	if strings.Contains(r.Combined(), "is a script") {
		t.Errorf("UsernsOnly still warns:\n%s", r.Combined())
	}
	var out string
	for _, e := range parseEvents(t, r) {
		if ps, _ := e["paths"].([]any); len(ps) > 0 && e.str("label") == "//apptest:script-userns" {
			out, _ = ps[0].(string)
		}
	}
	if out == "" {
		t.Fatalf("no output path for //apptest:script-userns")
	}
	home := t.TempDir()
	env := []string{"HOME=" + home, "XDG_CACHE_HOME=" + home, "BOB_ENTRY=deploy"}
	if rr := userNamespacesBlocked(t, env, onlyFile(t, out, "*.AppImage")); rr.Exit != 127 || !strings.Contains(rr.Stderr, "bobrun: this bundle needs user namespaces") {
		t.Errorf("UsernsOnly bundle without namespaces: exit %d, stderr %q, want 127 and `bobrun: this bundle needs user namespaces`", rr.Exit, rr.Stderr)
	}
}
