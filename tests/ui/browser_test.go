package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/megakuul/bob/tests/golden"
)

// nixpkgsPin is the nixpkgs revision Chromium comes from (HARNESS.md §1.4).
const nixpkgsPin = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"

// Environment knobs. tools/ci sets BOB_CHROMIUM when it already realised the browser.
const (
	envChromium = "BOB_CHROMIUM" // path to the chromium binary
	envAddr     = "BOB_UI_ADDR"  // listen address of the fixture server (default 127.0.0.1:0)
	envBuild    = "BOB_UI_BUILD" // static build to serve (default web/build)
)

// motionOff is the test-only CSS flag of HARNESS.md §9: it stops animations, transitions and
// the caret so screenshots do not depend on timing.
const motionOff = `*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}`

// env is shared by every test of one run: one fixture server and one browser (HARNESS.md §9).
type env struct {
	root, build string
	seed        *Seed
	url         string
	browser     context.Context
}

var (
	shared     *env
	sharedErr  error
	sharedOnce sync.Once
	stop       []func()
)

func TestMain(m *testing.M) {
	code := m.Run()
	for i := len(stop) - 1; i >= 0; i-- {
		stop[i]()
	}
	os.Exit(code)
}

// setup returns the shared environment. Failing to start it is a harness error, reported as
// such so it is never mistaken for a red page.
func setup(t *testing.T) *env {
	t.Helper()
	sharedOnce.Do(func() { shared, sharedErr = start() })
	if sharedErr != nil {
		t.Fatalf("harness: %v", sharedErr)
	}
	return shared
}

func start() (*env, error) {
	root, err := golden.Root()
	if err != nil {
		return nil, err
	}
	e := &env{root: root, build: os.Getenv(envBuild)}
	if e.build == "" {
		e.build = filepath.Join(root, "web", "build")
	}
	if _, err := os.Stat(filepath.Join(e.build, "200.html")); err != nil {
		return nil, fmt.Errorf("%s/200.html missing; run `go run ./tools/ci -only web` first: %w", e.build, err)
	}
	if e.seed, err = LoadSeed(filepath.Join(root, "tests", "ui", "seed.json")); err != nil {
		return nil, err
	}

	addr := os.Getenv(envAddr)
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("fixture server: %w", err)
	}
	srv := &http.Server{Handler: Handler(e.build, e.seed), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	stop = append(stop, func() { srv.Close() })
	e.url = "http://" + ln.Addr().String()

	bin, err := chromium(root)
	if err != nil {
		return nil, err
	}
	fonts, err := fontConfig(root)
	if err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(bin),
		chromedp.UserDataDir(fonts.dir),
		chromedp.Env("FONTCONFIG_FILE="+fonts.file, "TZ=UTC", "LANG=C.UTF-8"),
		chromedp.WindowSize(int(Sizes[0].Width), int(Sizes[0].Height)),
		chromedp.Flag("force-device-scale-factor", "1"),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-lcd-text", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("hide-scrollbars", true),
	)
	if os.Geteuid() == 0 {
		// Chromium refuses its sandbox as root; the harness host runs tests as root.
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	browser, cancelBrowser := chromedp.NewContext(alloc)
	stop = append(stop, func() { os.RemoveAll(fonts.dir) }, cancelAlloc, cancelBrowser)
	if err := chromedp.Do(browser); err != nil {
		return nil, fmt.Errorf("start chromium %s: %w", bin, err)
	}
	e.browser = browser
	return e, nil
}

// chromium returns the pinned headless browser, realising it from nixpkgs when needed.
func chromium(root string) (string, error) {
	if p := os.Getenv(envChromium); p != "" {
		return p, nil
	}
	out, err := nixBuild(root, "chromium")
	if err != nil {
		return "", err
	}
	return filepath.Join(out, "bin", "chromium"), nil
}

// nixBuild realises attr from the pinned nixpkgs and returns its store path.
func nixBuild(root, attr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", "--extra-experimental-features", "nix-command flakes",
		"build", "--no-link", "--print-out-paths", "github:NixOS/nixpkgs/"+nixpkgsPin+"#"+attr)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix build %s: %w: %s", attr, err, stderr.String())
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", fmt.Errorf("nix build %s printed no store path", attr)
	}
	return f[len(f)-1], nil
}

type fonts struct{ dir, file string }

// fontConfig writes a fontconfig file that lists only web/static/fonts, so screenshots never
// depend on the host's fonts (HARNESS.md §9). The directory also serves as the profile dir.
//
// Until web/ bundles its fonts, the pinned nixpkgs DejaVu fonts stand in: without any font
// Chromium paints no text at all, which makes every screenshot blank and FCP never fire.
// Goldens taken with the stand-in change once the bundled fonts land and are re-approved.
func fontConfig(root string) (fonts, error) {
	dirs := []string{filepath.Join(root, "web", "static", "fonts")}
	if !hasFonts(dirs[0]) {
		dejavu, err := nixBuild(root, "dejavu_fonts")
		if err != nil {
			return fonts{}, err
		}
		dirs = append(dirs, dejavu)
	}
	dir, err := os.MkdirTemp("", "bob-ui-")
	if err != nil {
		return fonts{}, fmt.Errorf("profile dir: %w", err)
	}
	var conf strings.Builder
	conf.WriteString("<?xml version=\"1.0\"?>\n<!DOCTYPE fontconfig SYSTEM \"fonts.dtd\">\n<fontconfig>")
	for _, d := range dirs {
		fmt.Fprintf(&conf, "<dir>%s</dir>", d)
	}
	fmt.Fprintf(&conf, "<cachedir>%s</cachedir></fontconfig>\n", filepath.Join(dir, "fc-cache"))
	f := filepath.Join(dir, "fonts.conf")
	if err := os.WriteFile(f, []byte(conf.String()), 0o644); err != nil {
		return fonts{}, fmt.Errorf("fonts.conf: %w", err)
	}
	return fonts{dir: dir, file: f}, nil
}

func hasFonts(dir string) bool {
	found := false
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".ttf", ".otf", ".woff", ".woff2":
			found = true
		}
		return nil
	})
	return found
}

// tab is one browser tab with console error capture.
type tab struct {
	ctx    context.Context
	mu     sync.Mutex
	errors []string
}

// newTab opens a tab with the fixed clock, the motion flag, the given theme preset in
// storage ("" leaves storage empty) and the OS preferring dark, so R-UI-004's "light by
// default" is exercised on every page.
func (e *env) newTab(t *testing.T, theme string, size Size) *tab {
	t.Helper()
	ctx, cancel := chromedp.NewContext(e.browser)
	t.Cleanup(cancel)
	tb := &tab{ctx: ctx}
	// Console subscribes when called, so nothing the page logs from its first load is lost.
	msgs := chromedp.Console(ctx)
	go func() {
		for m, err := range msgs {
			if err != nil {
				return
			}
			if m.Type == chromedp.ConsoleError || m.Type == chromedp.ConsoleException {
				tb.mu.Lock()
				tb.errors = append(tb.errors, m.String())
				tb.mu.Unlock()
			}
		}
	}()
	storage := `localStorage.removeItem("bob.theme");`
	if theme != "" {
		storage = fmt.Sprintf(`localStorage.setItem("bob.theme", %q);`, theme)
	}
	clock, _ := json.Marshal(e.seed.Clock)
	boot := fmt.Sprintf(`(() => {
  try { %s } catch (e) {}
  const T = Date.parse(%s), D = Date;
  globalThis.Date = class extends D { constructor(...a) { super(...(a.length ? a : [T])); } static now() { return T; } };
  document.addEventListener("DOMContentLoaded", () => {
    document.documentElement.setAttribute("data-test-motion", "off");
    const s = document.createElement("style"); s.textContent = %q; document.head.appendChild(s);
  });
})();`, storage, clock, motionOff)
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		if _, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: boot}); err != nil {
			return err
		}
		if _, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "dark"}}}); err != nil {
			return err
		}
		if _, err := cdp.Call(ctx, tg, emulation.SetTimezoneOverride, emulation.SetTimezoneOverrideParams{TimezoneID: "UTC"}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, tg, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width: size.Width, Height: size.Height, DeviceScaleFactor: 1, Mobile: size.Name == "phone"})
		return err
	}))
	if err != nil {
		t.Fatalf("harness: open tab: %v", err)
	}
	// The tab exists now; a deadline from here on bounds every later action without
	// closing the tab early (chromedp ties a tab's life to the context of its first Run).
	tctx, cancelT := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(cancelT)
	tb.ctx = tctx
	return tb
}

// open navigates to path and waits until the page settled: its data-page marker (at most 3 s; half a
// second when id is empty), fonts loaded and two frames painted.
func (tb *tab) open(e *env, path, id string) (string, error) {
	err := chromedp.Do(tb.ctx,
		chromedp.Navigate(e.url+path),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			if id == "" {
				// No marker to wait for: give the client router a moment to hydrate.
				return chromedp.Do(ctx, chromedp.Sleep(500*time.Millisecond))
			}
			marker := chromedp.Evaluate[bool](fmt.Sprintf(`!!document.querySelector('[data-page=%q]')`, id))
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
				found, err := marker(ctx, tg)
				if err != nil || found {
					return err
				}
				time.Sleep(50 * time.Millisecond)
			}
			return nil
		}),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			_, err := chromedp.Evaluate[bool](`document.fonts.ready.then(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))))`,
				chromedp.EvalAwaitPromise)(ctx, tg)
			return err
		}),
	)
	if err != nil {
		return "", err
	}
	return chromedp.Run(tb.ctx, chromedp.Evaluate[string](`document.documentElement.outerHTML`))
}

func (tb *tab) consoleErrors() []string {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return append([]string(nil), tb.errors...)
}

func (tb *tab) screenshot() (image.Image, []byte, error) {
	buf, err := chromedp.Run(tb.ctx, chromedp.CaptureScreenshot())
	if err != nil {
		return nil, nil, err
	}
	img, err := png.Decode(bytes.NewReader(buf))
	return img, buf, err
}

// assertGolden compares a screenshot with tests/goldens/ui/<name>.png. A mismatch writes the
// difference image to out/ui/ and the screenshot as a golden candidate (HARNESS.md §3.2); in
// update mode it writes only the candidate and does not fail.
func (e *env) assertGolden(t *testing.T, name string, got image.Image, raw []byte) {
	t.Helper()
	rel := filepath.Join("tests", "goldens", "ui", name+".png")
	wantImg, err := readPNG(filepath.Join(e.root, rel))
	if err == nil {
		d, cerr := Compare(wantImg, got)
		if cerr == nil && d.OK() {
			return
		}
		if golden.Updating() {
			e.candidate(t, rel, raw)
			return
		}
		if cerr != nil {
			e.candidate(t, rel, raw)
			t.Errorf("%s: %v", rel, cerr)
			return
		}
		diff := filepath.Join(e.root, "out", "ui", name+".diff.png")
		if werr := writePNG(diff, d.Image); werr != nil {
			t.Errorf("harness: %v", werr)
		}
		e.candidate(t, rel, raw)
		t.Errorf("%s: %d of %d pixels (%.3f%%) differ by more than %d, tolerance %.1f%%; diff %s",
			rel, d.Over, d.Pixels, 100*d.Ratio, ChannelDelta, 100*MaxDiffRatio, diff)
		return
	}
	e.candidate(t, rel, raw)
	if !golden.Updating() {
		t.Errorf("%s: no approved golden (%v); candidate written", rel, err)
	}
}

func (e *env) candidate(t *testing.T, rel string, raw []byte) {
	t.Helper()
	dir := os.Getenv(golden.EnvCandidates)
	if dir == "" {
		dir = filepath.Join(e.root, "out", "golden-candidates")
	}
	dst := filepath.Join(dir, rel)
	meta, _ := json.MarshalIndent(golden.Meta{Golden: filepath.ToSlash(rel), Test: t.Name(), Reqs: []string{"R-UI-002", "R-UI-003"}}, "", "  ")
	for _, w := range []struct {
		path string
		b    []byte
	}{{dst, raw}, {filepath.Join(dir, golden.MetaDir, rel+".json"), append(meta, '\n')}} {
		if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
			t.Errorf("harness: %v", err)
			return
		}
		if err := os.WriteFile(w.path, w.b, 0o644); err != nil {
			t.Errorf("harness: %v", err)
			return
		}
	}
}

func readPNG(file string) (image.Image, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func writePNG(file string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("diff dir: %w", err)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return fmt.Errorf("encode %s: %w", file, err)
	}
	return os.WriteFile(file, b.Bytes(), 0o644)
}
