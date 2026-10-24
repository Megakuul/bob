package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/megakuul/bob/tests/parity"
)

// Markup contract of the build page (/p/<project>/builds/<id>, SPEC §6.10.3), beyond the
// generic one in ui.go. Every section root carries data-section; rows are tbody > tr:
//
//	[data-section=packages]  one row per job, state text in the row, error code under a failed row;
//	                         a [data-view=graph] button switches to an <svg> with one [data-node]
//	                         (value = label) per package
//	[data-section=waterfall] one [data-bar=<label>] per finished or running package, width > 0
//	[data-section=artifacts] one row per artifact: store path, size (MiB), image ref
//	[data-section=log]       lines as [data-line]; text is inserted as text nodes
const (
	liveLineBudget = time.Second // R-NFR-105 / R-LOG-002: live line to screen p99 <= 1 s
	buildPath      = "/p/" + seedProject + "/builds/" + seedBuild
)

// TestBuildPage is the build-page half of R-UI-002: the graph table, waterfall bars and
// artifacts render the seeded data, and live log lines appear within the R-LOG budget. The
// seeded jobs and logs are asserted through the API first.
func TestBuildPage(t *testing.T) {
	parity.Covers(t, "R-UI-002")
	e := setup(t)
	checkAPI(t, e, Page{RPCs: []string{
		"/bob.build.v1.BuildService/GetBuild", "/bob.build.v1.BuildService/GetBuildGraph", "/bob.log.v1.LogService/GetLog", "/bob.cache.v1.CacheService/GetPath",
	}, IDs: []string{"//svc/api:server", "//lib/db:db", "//lib/auth:test", "BOB-E-2005", "compiling internal/http", "1258291"}})

	tb := e.newTab(t, "", Sizes[0])
	html, err := tb.open(e, buildPath, "build")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	if NotImplemented([]byte(html), "build") {
		t.Fatalf("not implemented: %s renders no data-page=\"build\"", buildPath)
	}
	text := func(sel string) []string {
		v, err := chromedp.Run(tb.ctx, chromedp.Evaluate[[]string](
			fmt.Sprintf(`[...document.querySelectorAll(%q)].map(n => n.innerText)`, sel)))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		return v
	}
	attrs := func(sel, attr string) []string {
		v, err := chromedp.Run(tb.ctx, chromedp.Evaluate[[]string](
			fmt.Sprintf(`[...document.querySelectorAll(%q)].map(n => n.getAttribute(%q) || '')`, sel, attr)))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		return v
	}

	// Packages table: one row per seeded job with its state; the failure row carries the code.
	rows := text(`[data-section=packages] tbody > tr`)
	for _, want := range [][2]string{{"//svc/api:image", "BUILDING"}, {"//lib/db:db", "CACHED"}, {"//lib/auth:test", "FAILED"}} {
		found := false
		for _, r := range rows {
			found = found || (strings.Contains(r, want[0]) && strings.Contains(r, want[1]))
		}
		if !found {
			t.Errorf("R-UI-002: packages table has no row with %q and state %s: %q", want[0], want[1], rows)
		}
	}
	if !strings.Contains(strings.Join(rows, "\n"), "BOB-E-2005") {
		t.Errorf("R-UI-002: packages table does not show the failed package's error code BOB-E-2005: %q", rows)
	}

	// Graph mode: one node per package.
	if err := chromedp.Do(tb.ctx, chromedp.Click(chromedp.CSS(`[data-section=packages] [data-view="graph"]`))); err != nil {
		t.Errorf("R-UI-002: graph toggle [data-view=graph]: %v", err)
	} else if nodes := attrs(`[data-section=packages] svg [data-node]`, "data-node"); len(nodes) != 4 {
		t.Errorf("R-UI-002: graph mode shows %d nodes %q, want 4", len(nodes), nodes)
	}

	// Waterfall: a bar with positive width for each of the four seeded packages.
	bars := attrs(`[data-section=waterfall] [data-bar]`, "data-bar")
	if len(bars) != 4 {
		t.Errorf("R-UI-002: waterfall has %d bars %q, want 4", len(bars), bars)
	}
	widths, err := chromedp.Run(tb.ctx, chromedp.Evaluate[[]float64](
		`[...document.querySelectorAll('[data-section=waterfall] [data-bar]')].map(n => n.getBoundingClientRect().width)`))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	for i, w := range widths {
		if w <= 0 {
			t.Errorf("R-UI-002: waterfall bar %q has width %v", bars[i], w)
		}
	}

	// Artifacts: store path, size and image ref of the seeded artifact.
	art := strings.Join(text(`[data-section=artifacts] tbody > tr`), "\n")
	for _, want := range []string{"/nix/store/0c3v5n7r9t1y6k2p4m8x0b1d3f5h7j9l-api-server", "1.2 MiB", seedRepo + ":b-" + seedBuild} {
		if !strings.Contains(art, want) {
			t.Errorf("R-UI-002: artifacts section does not show %q: %q", want, art)
		}
	}

	// Log: persisted and streamed lines are there; a line released now shows within the budget.
	lines := func() string { return strings.Join(text(`[data-section=log] [data-line]`), "\n") }
	waitFor := func(s string, within time.Duration) bool {
		for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			if strings.Contains(lines(), s) {
				return true
			}
		}
		return strings.Contains(lines(), s)
	}
	if !waitFor("0041 compiling internal/http", 3*time.Second) || !waitFor("0042 linking server", 3*time.Second) {
		t.Errorf("R-UI-002: log section lacks the persisted/streamed lines: %q", lines())
	}
	if strings.Contains(lines(), "LIVE-LINE-LATE") {
		t.Fatalf("harness: late line visible before release")
	}
	resp, err := insecure.Post(e.url+"/__test/release", "text/plain", nil)
	if err != nil {
		t.Fatalf("harness: release: %v", err)
	}
	resp.Body.Close()
	released := time.Now()
	if !waitFor("0043 LIVE-LINE-LATE", 5*time.Second) {
		t.Fatalf("R-UI-002: a log line streamed by FollowLog never appeared")
	}
	if d := time.Since(released); d > liveLineBudget+250*time.Millisecond { // + polling and CDP round trip
		t.Errorf("R-UI-002: live log line took %s to reach the screen, budget %s (R-NFR-105)", d, liveLineBudget)
	}
	for _, msg := range tb.consoleErrors() {
		t.Errorf("R-UI-002: %s: %s", buildPath, msg)
	}
}

// TestStreamMock proves the fixture's FollowLog stream: the seeded first step arrives at once and
// the held-back step only after POST /__test/release, as connect+json envelopes.
func TestStreamMock(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/bob.log.v1.LogService/FollowLog", strings.NewReader("\x00\x00\x00\x00\x02{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/connect+json")
	resp, err := insecure.Do(req)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	defer resp.Body.Close()
	next := func() string {
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(resp.Body, hdr); err != nil {
			t.Fatalf("harness: envelope header: %v", err)
		}
		body := make([]byte, int(hdr[1])<<24|int(hdr[2])<<16|int(hdr[3])<<8|int(hdr[4]))
		if _, err := io.ReadFull(resp.Body, body); err != nil {
			t.Fatalf("harness: envelope body: %v", err)
		}
		return string(body)
	}
	if m := next(); !strings.Contains(m, "0042 linking server") {
		t.Fatalf("harness: first message %s", m)
	}
	r, err := insecure.Post(e.url+"/__test/release", "text/plain", nil)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	r.Body.Close()
	if m := next(); !strings.Contains(m, "LIVE-LINE-LATE") {
		t.Fatalf("harness: second message %s", m)
	}
}
