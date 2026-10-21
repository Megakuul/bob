// Package ui is the UI test harness (HARNESS.md §9, suite `ui`): a registry of every page of
// SPEC §6.10.3, a fixture server that serves the static build of web/ together with a mocked
// public API answered from tests/ui/seed.json, and the pixel comparison against the approved
// screenshots in tests/goldens/ui/<page>.<theme>.<size>.png.
//
// The browser side (headless Chromium from the nixpkgs pin, driven through chromedp) lives in
// the _test.go files, so nothing outside tests/ui links chromedp.
//
// Until tools/devstack exists the API is mocked here: every connect RPC the UI calls is
// answered from seed.json by exact procedure path. An RPC without a seed answer gets the
// connect error `unimplemented`, which is how a stub server answers too.
//
// Markup contract the tests rely on (beyond SPEC §6.10):
//   - every page's root element carries data-page="<page id>" (pages without it count as
//     "not implemented", which is how every page starts before M-UI tasks land);
//   - a list's filter text input is input[name="query"] and its rows are tbody > tr;
//   - id cells carry class "id" and are <a href> elements (R-UI-006).
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Page is one route of SPEC §6.10.3.
type Page struct {
	ID    string   // golden file stem and data-page value
	Route string   // route pattern as SPEC writes it
	Path  string   // concrete path with seeded parameters
	IDs   []string // seeded entity ids (as text) the page must show (R-UI-002)
	RPCs  []string // connect procedures the page reads; asserted through the API first
	List  bool     // has a filter bar (R-UI-006)
	// Filter is a seeded id prefix that must reduce the list to matching rows.
	Filter string
}

// Seeded ids. Fixed so pages render the same every time.
const (
	seedProject = "api"
	seedBuild   = "01jbq7k2p4m8x0c3v5n7r9t1y6"
	seedFailed  = "01jbq7aa0d2f4h6k8m0p2r4t6v"
	seedWorker  = "w3kq8p0q"
	seedRepo    = "api-server"
	seedRelease = "1.4.2"
	seedUser    = "alice"
)

// Pages is every route of SPEC §6.10.3, in SPEC order. Tabs are covered by their page.
var Pages = []Page{
	{ID: "overview", Route: "/", Path: "/", IDs: []string{seedBuild, seedFailed},
		RPCs: []string{"/bob.analytics.v1.AnalyticsService/GetOverview", "/bob.build.v1.BuildService/ListBuilds"},
		List: true, Filter: "01jbq7k"},
	{ID: "projects", Route: "/projects", Path: "/projects", IDs: []string{seedProject, "web"},
		RPCs: []string{"/bob.project.v1.ProjectService/ListProjects"}, List: true, Filter: "ap"},
	{ID: "project", Route: "/p/<project>", Path: "/p/" + seedProject, IDs: []string{seedBuild},
		RPCs: []string{"/bob.project.v1.ProjectService/GetProject", "/bob.build.v1.BuildService/ListBuilds"},
		List: true, Filter: "01jbq7k"},
	{ID: "build", Route: "/p/<project>/builds/<id>", Path: "/p/" + seedProject + "/builds/" + seedBuild,
		IDs:  []string{seedBuild, "//svc/api:server"},
		RPCs: []string{"/bob.build.v1.BuildService/GetBuild", "/bob.build.v1.JobService/ListJobs"}},
	{ID: "fleet", Route: "/fleet", Path: "/fleet", IDs: []string{seedWorker, "o-1"},
		RPCs: []string{"/bob.fleet.v1.FleetService/ListWorkers", "/bob.fleet.v1.FleetService/ListOrchestrators"},
		List: true, Filter: "w3k"},
	{ID: "worker", Route: "/fleet/workers/<id>", Path: "/fleet/workers/" + seedWorker, IDs: []string{seedWorker},
		RPCs: []string{"/bob.fleet.v1.FleetService/GetWorker"}},
	{ID: "cache", Route: "/cache", Path: "/cache", IDs: []string{"//svc/api:server"},
		RPCs: []string{"/bob.cache.v1.CacheService/GetCacheStats", "/bob.cache.v1.CacheService/ListTopPackages"}},
	{ID: "registry", Route: "/registry", Path: "/registry", IDs: []string{seedRelease},
		RPCs: []string{"/bob.release.v1.ReleaseService/ListReleases", "/bob.registry.v1.RegistryService/ListRepositories"},
		List: true, Filter: "1.4"},
	{ID: "repository", Route: "/registry/<repo>", Path: "/registry/" + seedRepo, IDs: []string{seedRepo},
		RPCs: []string{"/bob.registry.v1.RegistryService/GetRepository", "/bob.registry.v1.RegistryService/ListTags"}},
	{ID: "release", Route: "/p/<project>/releases/<name>/<version>",
		Path: "/p/" + seedProject + "/releases/" + seedProject + "/" + seedRelease,
		IDs:  []string{"bob install acme/api@1.4.2"}, RPCs: []string{"/bob.release.v1.ReleaseService/GetRelease"}},
	{ID: "access", Route: "/access", Path: "/access", IDs: []string{seedUser},
		RPCs: []string{"/bob.access.v1.UserService/ListUsers"}, List: true, Filter: "ali"},
	{ID: "settings", Route: "/settings", Path: "/settings", IDs: []string{"bob-test"},
		RPCs: []string{"/bob.settings.v1.SettingsService/GetSettings"}},
	{ID: "analytics", Route: "/analytics", Path: "/analytics", IDs: []string{"//svc/api:server"},
		RPCs: []string{"/bob.analytics.v1.AnalyticsService/PackageDurations"}},
	{ID: "login", Route: "/login", Path: "/login", IDs: []string{"Corp SSO"},
		RPCs: []string{"/bob.auth.v1.AuthService/GetServerInfo"}},
	{ID: "device", Route: "/device", Path: "/device?user_code=BCDF-GHJK", IDs: []string{"BCDF-GHJK"}},
	{ID: "consent", Route: "/oauth/consent", Path: "/oauth/consent?client_id=bob-cli", IDs: []string{"bob-cli"}},
	{ID: "account", Route: "/account", Path: "/account", IDs: []string{seedUser},
		RPCs: []string{"/bob.auth.v1.AuthService/WhoAmI"}},
}

// Theme and size axes of the screenshot matrix (HARNESS.md §9).
var (
	Themes = []string{"light", "dark"}
	Sizes  = []Size{{"desktop", 1440, 900}, {"phone", 390, 844}}
)

// Size is a fixed browser window.
type Size struct {
	Name          string
	Width, Height int64
}

// Seed is the fixed fixture data set (tests/ui/seed.json).
type Seed struct {
	// Clock is the fixed "now" the browser's Date is pinned to.
	Clock string `json:"clock"`
	// RPC maps a connect procedure path to its JSON response message.
	RPC map[string]json.RawMessage `json:"rpc"`
}

// LoadSeed reads seed.json.
func LoadSeed(file string) (*Seed, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("ui: read seed: %w", err)
	}
	var s Seed
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("ui: parse seed %s: %w", file, err)
	}
	return &s, nil
}

// Handler serves the static build in dir with the R-UI-001 rules (unknown paths get 200.html
// with no-cache, /_app/immutable/* is immutable) and answers connect RPCs from the seed.
func Handler(dir string, seed *Seed) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		if strings.HasPrefix(p, "/bob.") {
			serveRPC(w, r, p, seed)
			return
		}
		if strings.HasPrefix(p, "/_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && !fi.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, "200.html"))
	})
}

// serveRPC answers unary connect calls (GET for lists per R-API-151, POST otherwise) with the
// seeded message, and everything else with the connect error a stub server returns.
func serveRPC(w http.ResponseWriter, r *http.Request, proc string, seed *Seed) {
	msg, ok := seed.RPC[proc]
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, `{"code":"unimplemented","message":"%s is not seeded"}`, proc)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(msg)
}

// Tolerance is the screenshot comparison bound of HARNESS.md §9: at most MaxDiffRatio of the
// pixels may have a channel delta above ChannelDelta. Protected after Gate H.
const (
	ChannelDelta = 16
	MaxDiffRatio = 0.001
)

// Diff is the result of comparing two screenshots.
type Diff struct {
	Pixels, Over int     // total pixels, pixels with a channel delta above ChannelDelta
	Ratio        float64 // Over / Pixels
	Image        *image.RGBA
}

// OK reports whether the difference is within tolerance.
func (d Diff) OK() bool { return d.Ratio <= MaxDiffRatio }

// Compare compares got with want pixel by pixel. Images of different size never match: the
// error says so instead of producing a diff.
func Compare(want, got image.Image) (Diff, error) {
	wb, gb := want.Bounds(), got.Bounds()
	if wb.Dx() != gb.Dx() || wb.Dy() != gb.Dy() {
		return Diff{}, fmt.Errorf("ui: size %dx%d, golden %dx%d", gb.Dx(), gb.Dy(), wb.Dx(), wb.Dy())
	}
	d := Diff{Pixels: wb.Dx() * wb.Dy(), Image: image.NewRGBA(image.Rect(0, 0, wb.Dx(), wb.Dy()))}
	for y := 0; y < wb.Dy(); y++ {
		for x := 0; x < wb.Dx(); x++ {
			wc := color.RGBAModel.Convert(want.At(wb.Min.X+x, wb.Min.Y+y)).(color.RGBA)
			gc := color.RGBAModel.Convert(got.At(gb.Min.X+x, gb.Min.Y+y)).(color.RGBA)
			if delta(wc.R, gc.R) > ChannelDelta || delta(wc.G, gc.G) > ChannelDelta ||
				delta(wc.B, gc.B) > ChannelDelta || delta(wc.A, gc.A) > ChannelDelta {
				d.Over++
				d.Image.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
				continue
			}
			// Unchanged pixels are kept faint so the red ones stand out in review.
			l := uint8((uint16(gc.R) + uint16(gc.G) + uint16(gc.B)) / 3)
			d.Image.SetRGBA(x, y, color.RGBA{R: 192 + l/4, G: 192 + l/4, B: 192 + l/4, A: 255})
		}
	}
	d.Ratio = float64(d.Over) / float64(d.Pixels)
	return d, nil
}

func delta(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// NotImplemented reports whether a rendered page is still the skeleton: no data-page marker
// for its id. Tests fail such pages with a "not implemented:" message, the only red reason
// HARNESS.md §15 accepts before the UI tasks land.
func NotImplemented(html []byte, id string) bool {
	return !bytes.Contains(html, []byte(`data-page="`+id+`"`))
}
