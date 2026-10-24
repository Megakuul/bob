package ui

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/megakuul/bob/tests/parity"
)

// TestPages runs every page of SPEC §6.10.3: API state first (the seeded data comes back
// through the public API), then the DOM (seeded ids shown, no console errors, id links, list
// filter), and only then screenshots for every theme × size against the goldens.
func TestPages(t *testing.T) {
	parity.Covers(t, "R-UI-002", "R-UI-006")
	e := setup(t)
	for _, p := range Pages {
		t.Run(p.ID, func(t *testing.T) {
			checkAPI(t, e, p)
			tb := e.newTab(t, "", Sizes[0])
			html, err := tb.open(e, p.Path, p.ID)
			if err != nil {
				t.Fatalf("harness: open %s: %v", p.Path, err)
			}
			if NotImplemented([]byte(html), p.ID) {
				t.Fatalf("not implemented: %s (%s) renders no data-page=%q", p.Path, p.Route, p.ID)
			}
			checkDOM(t, e, tb, p)
			if t.Failed() {
				return // screenshots of a page with wrong data would only hide the cause
			}
			for _, theme := range Themes {
				for _, size := range Sizes {
					st := e.newTab(t, theme, size)
					if _, err := st.open(e, p.Path, p.ID); err != nil {
						t.Fatalf("harness: open %s: %v", p.Path, err)
					}
					img, raw, err := st.screenshot()
					if err != nil {
						t.Fatalf("harness: screenshot: %v", err)
					}
					e.assertGolden(t, p.ID+"."+theme+"."+size.Name, img, raw)
				}
			}
		})
	}
}

// checkAPI asserts that the page's RPCs answer with the seeded ids before any browser work, so a
// red page points at the UI and not at the fixture data.
func checkAPI(t *testing.T, e *env, p Page) {
	t.Helper()
	var bodies []byte
	for _, rpc := range p.RPCs {
		u := e.url + rpc + "?connect=v1&encoding=json&message=" + url.QueryEscape("{}")
		resp, err := insecure.Get(u)
		if err != nil {
			t.Fatalf("harness: GET %s: %v", rpc, err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("harness: GET %s: status %d: %s %v", rpc, resp.StatusCode, b, err)
		}
		bodies = append(bodies, b...)
	}
	if len(p.RPCs) == 0 {
		return
	}
	for _, id := range p.IDs {
		if !bytes.Contains(bodies, []byte(id)) {
			t.Fatalf("harness: seed: %q not returned by %v", id, p.RPCs)
		}
	}
}

func checkDOM(t *testing.T, e *env, tb *tab, p Page) {
	t.Helper()
	text, err := chromedp.Run(tb.ctx, chromedp.Evaluate[string](`document.body.innerText`))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	for _, id := range append(append([]string(nil), p.IDs...), p.Text...) {
		if !strings.Contains(text, id) {
			t.Errorf("R-UI-002: %s does not show seeded %q", p.Path, id)
		}
	}
	for _, msg := range tb.consoleErrors() {
		t.Errorf("R-UI-002: %s: %s", p.Path, msg)
	}

	// R-UI-006: every .id cell is a link whose target renders the same id.
	links, err := chromedp.Run(tb.ctx, chromedp.Evaluate[[]struct{ Tag, Href, Text, Full string }](
		`[...document.querySelectorAll('.id')].map(n => ({Tag: n.tagName, Href: n.getAttribute('href') || '', Text: n.textContent.trim(), Full: n.getAttribute('title') || n.getAttribute('data-id') || ''}))`))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	seen := map[string]bool{}
	for _, l := range links {
		if l.Tag != "A" || l.Href == "" {
			t.Errorf("R-UI-006: %s: .id %q is a <%s> without href", p.Path, l.Text, strings.ToLower(l.Tag))
			continue
		}
		if seen[l.Href] {
			continue
		}
		seen[l.Href] = true // every distinct target is opened (SPEC: every cell with class id)
		lt := e.newTab(t, "", Sizes[0])
		if _, err := lt.open(e, l.Href, ""); err != nil {
			t.Fatalf("harness: open %s: %v", l.Href, err)
		}
		target, err := chromedp.Run(lt.ctx, chromedp.Evaluate[string](`document.body.innerText`))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		id := strings.TrimRight(l.Text, "…")
		if l.Full != "" {
			id = l.Full // a truncated cell carries the full id in title or data-id
		} else if id != l.Text && !strings.Contains(l.Href, id) {
			t.Errorf("R-UI-006: %s: truncated id %q is not part of its link target %s", p.Path, l.Text, l.Href)
		}
		if !strings.Contains(target, id) {
			t.Errorf("R-UI-006: %s: link %s does not render id %q", p.Path, l.Href, id)
		}
	}

	if !p.List {
		return
	}
	// R-UI-006: the filter reduces rows to matching ones and lands in the URL.
	rows := chromedp.Evaluate[[]string](`[...document.querySelectorAll('tbody > tr')].map(r => r.innerText)`)
	var before, after []string
	var loc string
	err = chromedp.Do(tb.ctx,
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) (err error) { before, err = rows(ctx, tg); return err }),
		chromedp.SendKeys(chromedp.CSS(`input[name="query"]`), p.Filter),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) (err error) { after, err = rows(ctx, tg); return err }),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) (err error) {
			loc, err = chromedp.Location()(ctx, tg)
			return err
		}),
	)
	if err != nil {
		t.Errorf("R-UI-006: %s: filter input[name=query]: %v", p.Path, err)
		return
	}
	matching := 0
	for _, r := range before {
		if strings.Contains(strings.ToLower(r), strings.ToLower(p.Filter)) {
			matching++
		}
	}
	// reduces rows TO the matching ones: exactly those that matched before survive
	if len(after) == 0 || len(after) != matching {
		t.Errorf("R-UI-006: %s: filter %q: %d rows before (%d matching), %d after", p.Path, p.Filter, len(before), matching, len(after))
	}
	for _, r := range after {
		if !strings.Contains(strings.ToLower(r), strings.ToLower(p.Filter)) {
			t.Errorf("R-UI-006: %s: filter %q kept row %q", p.Path, p.Filter, r)
		}
	}
	if u, err := url.Parse(loc); err != nil || u.Query().Get("query") != p.Filter {
		t.Errorf("R-UI-006: %s: URL %s does not carry query=%s", p.Path, loc, p.Filter)
	}
}

// TestThemeDefault is R-UI-004: OS prefers dark, storage empty → light; choosing Dark sticks
// across a reload. The theme switch carries data-theme-choice="light|dark|system".
func TestThemeDefault(t *testing.T) {
	parity.Covers(t, "R-UI-004")
	e := setup(t)
	tb := e.newTab(t, "", Sizes[0])
	if _, err := tb.open(e, "/", "overview"); err != nil {
		t.Fatalf("harness: %v", err)
	}
	theme, bg := themeState(t, tb)
	if theme == "" {
		t.Fatalf("not implemented: <html> has no data-theme attribute")
	}
	if theme != "light" || bg != "rgb(255, 255, 255)" {
		t.Fatalf("R-UI-004: empty storage, OS dark: data-theme=%q background %s, want light rgb(255, 255, 255)", theme, bg)
	}
	if err := chromedp.Do(tb.ctx, chromedp.Click(chromedp.CSS(`[data-theme-choice="dark"]`))); err != nil {
		t.Fatalf("R-UI-004: theme switch: %v", err)
	}
	if err := chromedp.Do(tb.ctx, chromedp.Reload()); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if theme, bg = themeState(t, tb); theme != "dark" || bg != "rgb(14, 14, 13)" {
		t.Fatalf("R-UI-004: after choosing Dark and reloading: data-theme=%q background %s, want dark rgb(14, 14, 13)", theme, bg)
	}
}

func themeState(t *testing.T, tb *tab) (theme, bg string) {
	t.Helper()
	theme, err := chromedp.Run(tb.ctx, chromedp.Evaluate[string](`document.documentElement.getAttribute('data-theme') || ''`))
	if err == nil {
		bg, err = chromedp.Run(tb.ctx, chromedp.Evaluate[string](`getComputedStyle(document.body).backgroundColor`))
	}
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return theme, bg
}

// TestBuildShape is the static half of R-UI-001: the build is adapter-static output with a
// 200.html fallback. The header half runs against bob-server in server-e2e.
func TestBuildShape(t *testing.T) {
	e := setup(t)
	if _, err := os.Stat(filepath.Join(e.root, "web", "build", "_app", "immutable")); err != nil {
		t.Errorf("web/build has no _app/immutable: %v", err)
	}
	for _, bad := range []string{"server", "index.js", "handler.js"} {
		if _, err := os.Stat(filepath.Join(e.root, "web", "build", bad)); err == nil {
			t.Errorf("web/build/%s exists: the build must be static", bad)
		}
	}
}

// Initial-load caps of R-UI-007 (gzip) and the overview FCP budget on [8c/15G].
const (
	maxJSGzip   = 250 << 10
	maxCSSGzip  = 30 << 10
	maxFCP      = time.Second     // [8c/15G]
	maxFCPSmall = 2 * time.Second // [2c/4G]
)

var assetRef = regexp.MustCompile(`(?:href|src)="(/_app/[^"]+\.(?:js|css))"|import\("(/_app/[^"]+\.js)"\)`)

// TestFirstLoadBudget is R-UI-007: gzip size of what 200.html loads up front, and the
// overview's first contentful paint from PerformanceObserver.
func TestFirstLoadBudget(t *testing.T) {
	parity.Covers(t, "R-UI-007")
	e := setup(t)
	html, err := os.ReadFile(filepath.Join(e.build, "200.html"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	sizes := map[string]int{}
	seen := map[string]bool{}
	for _, m := range assetRef.FindAllStringSubmatch(string(html), -1) {
		ref := m[1] + m[2]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		n, err := gzipSize(filepath.Join(e.build, filepath.FromSlash(ref)))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		sizes[filepath.Ext(ref)] += n
	}
	if sizes[".js"] > maxJSGzip {
		t.Errorf("R-UI-007: initial JS %d B gzip > %d", sizes[".js"], maxJSGzip)
	}
	if sizes[".css"] > maxCSSGzip {
		t.Errorf("R-UI-007: initial CSS %d B gzip > %d", sizes[".css"], maxCSSGzip)
	}

	tb := e.newTab(t, "", Sizes[0])
	html0, err := tb.open(e, "/", "overview")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	if NotImplemented([]byte(html0), "overview") {
		t.Fatalf("not implemented: / renders no data-page=\"overview\"; the paint of a skeleton proves nothing")
	}
	t.Logf("first load: js %d B gz, css %d B gz", sizes[".js"], sizes[".css"])
	fcp, err := chromedp.Run(tb.ctx, chromedp.Evaluate[float64](
		`(performance.getEntriesByName('first-contentful-paint')[0] || {startTime: -1}).startTime`))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	// what the overview really loaded up front (resource timing), not only what the shell references:
	// every script and stylesheet fetched before the page's load event counts against the budgets
	names, err := chromedp.Run(tb.ctx, chromedp.Evaluate[string](
		`JSON.stringify((() => { const nav = performance.getEntriesByType('navigation')[0];
		  const end = nav ? Math.max(nav.loadEventEnd, nav.domContentLoadedEventEnd) : Infinity;
		  return performance.getEntriesByType('resource').filter(r => r.responseEnd <= end + 1000 &&
		    /\.(js|css|mjs)$/.test(new URL(r.name).pathname)).map(r => new URL(r.name).pathname); })())`))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	var loaded []string
	if err := json.Unmarshal([]byte(names), &loaded); err != nil {
		t.Fatalf("harness: %v", err)
	}
	live := map[string]int{}
	for _, p := range loaded {
		rel := strings.TrimPrefix(p, "/")
		if seen[rel] {
			continue
		}
		seen[rel] = true
		n, err := gzipSize(filepath.Join(e.build, filepath.FromSlash(rel)))
		if err != nil {
			t.Logf("resource %s is not a build file (%v); not counted", p, err)
			continue
		}
		live[filepath.Ext(rel)] += n
	}
	if sizes[".js"]+live[".js"] > maxJSGzip {
		t.Errorf("R-UI-007: JS loaded by the overview %d B gzip (shell %d + loaded later %d) > %d", sizes[".js"]+live[".js"], sizes[".js"], live[".js"], maxJSGzip)
	}
	if sizes[".css"]+live[".css"] > maxCSSGzip {
		t.Errorf("R-UI-007: CSS loaded by the overview %d B gzip (shell %d + loaded later %d) > %d", sizes[".css"]+live[".css"], sizes[".css"], live[".css"], maxCSSGzip)
	}
	budget := maxFCPSmall
	if runtime.NumCPU() >= 8 && memGiB() >= 15 {
		budget = maxFCP
	}
	t.Logf("overview FCP %.0f ms, budget %s", fcp, budget)
	if fcp < 0 || time.Duration(fcp*float64(time.Millisecond)) > budget {
		t.Errorf("R-UI-007: overview first contentful paint %.0f ms, budget %s", fcp, budget)
	}
}

// memGiB is the host's physical memory in GiB (0 when unknown).
func memGiB() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseFloat(f[1], 64)
			return kb / (1 << 20)
		}
	}
	return 0
}

func gzipSize(file string) (int, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("gzip %s: %w", file, err)
	}
	return buf.Len(), nil
}

// TestHarnessDeterministic proves the browser half of the harness works on this host: the
// same page shot twice is within tolerance of itself, in both themes and sizes.
func TestHarnessDeterministic(t *testing.T) {
	e := setup(t)
	for _, size := range Sizes {
		for _, theme := range Themes {
			var shots [2]image.Image
			for i := range shots {
				tb := e.newTab(t, theme, size)
				if _, err := tb.open(e, "/", ""); err != nil {
					t.Fatalf("harness: %v", err)
				}
				img, _, err := tb.screenshot()
				if err != nil {
					t.Fatalf("harness: %v", err)
				}
				if b := img.Bounds(); int64(b.Dx()) != size.Width || int64(b.Dy()) != size.Height {
					t.Fatalf("harness: %s screenshot is %dx%d", size.Name, b.Dx(), b.Dy())
				}
				shots[i] = img
			}
			d, err := Compare(shots[0], shots[1])
			if err != nil || !d.OK() {
				t.Errorf("harness: %s/%s not deterministic: %v ratio %.4f", theme, size.Name, err, d.Ratio)
			}
		}
	}
}

func TestCompare(t *testing.T) {
	solid := func(w, h int, c color.RGBA, dirty int, dc color.RGBA) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			p := c
			if i < dirty {
				p = dc
			}
			img.SetRGBA(i%w, i/w, p)
		}
		return img
	}
	white := color.RGBA{255, 255, 255, 255}
	tests := []struct {
		name    string
		want    image.Image
		got     image.Image
		ok      bool
		sizeErr bool
	}{
		{"identical", solid(100, 100, white, 0, white), solid(100, 100, white, 0, white), true, false},
		{"jitter within channel delta", solid(100, 100, white, 0, white), solid(100, 100, white, 10000, color.RGBA{240, 240, 240, 255}), true, false},
		{"10 of 10000 pixels over", solid(100, 100, white, 0, white), solid(100, 100, white, 10, color.RGBA{0, 0, 0, 255}), true, false},
		{"11 of 10000 pixels over", solid(100, 100, white, 0, white), solid(100, 100, white, 11, color.RGBA{0, 0, 0, 255}), false, false},
		{"size differs", solid(100, 100, white, 0, white), solid(100, 99, white, 0, white), false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Compare(tc.want, tc.got)
			if (err != nil) != tc.sizeErr {
				t.Fatalf("err = %v, want size error %v", err, tc.sizeErr)
			}
			if err == nil && d.OK() != tc.ok {
				t.Fatalf("OK() = %v (over %d), want %v", d.OK(), d.Over, tc.ok)
			}
		})
	}
}

func TestRegistryCoversSpecRoutes(t *testing.T) {
	// SPEC §6.10.3 route list; a new route there must get a page here.
	want := []string{"/", "/projects", "/p/<project>", "/p/<project>/builds/<id>", "/fleet",
		"/fleet/workers/<id>", "/cache", "/registry", "/registry/<repo>",
		"/p/<project>/releases/<name>/<version>", "/access", "/settings", "/analytics", "/login",
		"/device", "/oauth/consent", "/account"}
	got := map[string]bool{}
	ids := map[string]bool{}
	for _, p := range Pages {
		got[p.Route] = true
		if ids[p.ID] {
			t.Errorf("duplicate page id %q", p.ID)
		}
		ids[p.ID] = true
	}
	for _, r := range want {
		if !got[r] {
			t.Errorf("route %s has no page in Pages", r)
		}
	}
	if len(Pages) != len(want) {
		t.Errorf("%d pages, SPEC lists %d routes", len(Pages), len(want))
	}
}
