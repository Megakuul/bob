package npm

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
)

// devSession prepares a copy of repo dir for an unprivileged user, sets the Vite dev port in
// importerDir/bob.go, locks, and starts `bob dev --servers-only`. ok is false when the feature
// is not built yet (t is red) or the server did not come up.
func devSession(t *testing.T, dir, importerDir string) (p *devProc, port int, env []string, uid, gid uint32, ok bool) {
	t.Helper()
	uid, gid = ecotest.Unprivileged(t)
	home := t.TempDir()
	env = ecotest.Registry(t, home)
	var err error
	if port, err = serverenv.FreePort(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	bobgo := filepath.Join(dir, importerDir, "bob.go")
	ecotest.ReplaceIn(t, bobgo, "npm.ViteOpts{", fmt.Sprintf("npm.ViteOpts{Dev: npm.DevServer{Port: %d}, ", port))
	ecotest.ChownTree(t, dir, uid, gid)
	ecotest.ChownTree(t, home, uid, gid)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return nil, 0, nil, uid, gid, false
	}
	p = startDev(t, uid, gid, dir, env, "dev", "--servers-only")
	if !p.waitOutput(regexp.MustCompile(`ready at http://\S+`), 3*time.Minute) {
		if out := p.output(); strings.Contains(out, harness.NotImplemented) {
			harness.RedFeature(t, "bob dev --servers-only -> "+strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
		} else {
			t.Errorf("bob dev --servers-only printed no readiness line:\n%s", out)
		}
		return p, port, env, uid, gid, false
	}
	return p, port, env, uid, gid, true
}

// R-NPM-010a (HMR, dependency restart): in the Vite fixture under bob dev as non-root, saving
// the Svelte component (the fixture is Vite+Svelte; the SPEC says App.tsx) emits an HMR update
// over the dev-server websocket (end to end within 2 s; the 50 ms server-side budget B-082 is
// measured by the budgets stage), and adding a dependency in bob.go restarts Vite (old process gone, new one
// answering) with the new package importable through a served module within 10 s.
func TestRNPM010aHMRAndDependencyRestart(t *testing.T) {
	parity.Covers(t, "R-NPM-010a")
	dir := harness.Fixture(t, "npm")
	p, port, _, uid, gid, ok := devSession(t, dir, "site")
	if !ok {
		return
	}
	if _, err := waitGet(port, "/", regexp.MustCompile(`<script`), 2*time.Second); err != nil {
		t.Fatalf("GET / does not answer with the app's index.html: %v\n%s", err, p.output())
	}
	client, err := waitGet(port, "/@vite/client", wsToken, 5*time.Second)
	if err != nil {
		t.Fatalf("GET /@vite/client: %v", err)
	}
	ws, err := wsDial(port, wsToken.FindStringSubmatch(client)[1])
	if err != nil {
		t.Fatalf("HMR websocket: %v", err)
	}
	defer func() { _ = ws.conn.Close() }()
	if m, err := ws.next(5 * time.Second); err != nil || !strings.Contains(m, `"connected"`) {
		t.Fatalf("first websocket message = %q, %v; want the connected message", m, err)
	}

	// Saving src/App.svelte (as the user would, from outside) emits an update.
	app := filepath.Join(dir, "site", "src", "App.svelte")
	ecotest.ReplaceIn(t, app, "<p>{greet", "<p>edited {greet")
	_ = os.Chown(app, int(uid), int(gid))
	deadline := time.Now().Add(2 * time.Second)
	updated := false
	for !updated && time.Now().Before(deadline) {
		m, err := ws.next(time.Until(deadline))
		if err != nil {
			break
		}
		updated = strings.Contains(m, `"type":"update"`) && strings.Contains(m, "App.svelte")
	}
	if !updated {
		t.Errorf("no HMR update for src/App.svelte within 2 s of the save\n%s", p.output())
	}

	// Adding a dependency restarts Vite. The old process must go away (its HMR websocket
	// closes or receives a full-reload), a new one must answer, and a module importing the
	// new package must be served with the package resolved: all within 10 s of the edit.
	bobgo := filepath.Join(dir, "site", "bob.go")
	start := time.Now()
	ecotest.ReplaceIn(t, bobgo, `npm.Dep("svelte", "^5"),`, `npm.Dep("svelte", "^5"),
			npm.Dep("left-pad", "^1.3.0"),`)
	_ = os.Chown(bobgo, int(uid), int(gid))
	restarted := false
	for !restarted && time.Since(start) < 10*time.Second {
		m, err := ws.next(10*time.Second - time.Since(start))
		// A read error means the old server's connection closed (it was stopped); a
		// full-reload is Vite's own signal after a config or dependency change.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			break // nothing happened: not restarted
		}
		restarted = err != nil || strings.Contains(m, `"type":"full-reload"`)
	}
	if !restarted {
		t.Errorf("Vite was not restarted within 10 s of the bob.go edit: its websocket neither closed nor sent full-reload\n%s", p.output())
		return
	}
	probe := filepath.Join(dir, "site", "src", "leftpad.js")
	ecotest.WriteFile(t, probe, "import leftPad from \"left-pad\";\nexport default leftPad;\n")
	_ = os.Chown(probe, int(uid), int(gid))
	body, err := waitGet(port, "/src/leftpad.js", regexp.MustCompile(`from "(/[^"]*left-pad[^"]*)"`), 10*time.Second-time.Since(start))
	if err != nil {
		t.Errorf("the module importing left-pad is not served with the package resolved within 10 s of the bob.go edit: %v\n%s", err, p.output())
		return
	}
	dep := regexp.MustCompile(`from "(/[^"]*left-pad[^"]*)"`).FindStringSubmatch(body)[1]
	if _, err := waitGet(port, dep, nil, 10*time.Second-time.Since(start)); err != nil {
		t.Errorf("the resolved left-pad module %s is not served by the restarted Vite: %v\n%s", dep, err, p.output())
	}
}

const siteGo = `//go:build bob

package gensite

import (
	greetv1 "example.com/codegen/proto/greet/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/npm"
)

// Site is a Vite site that imports the generated connect-es code.
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{
		Deps: []npm.Dependency{npm.Dep("vite", "^7.1.0", npm.Dev())},
		Generated: []npm.Generated{connect.TS(greetv1.Greet(p), connect.TSOpts{})},
	})
})
`

// R-NPM-010a (codegen): editing a .proto used via connect.TS in a Vite importer changes the
// served module within 10 s (the generated file is regenerated into the tree and reaches Vite
// through its watcher).
func TestRNPM010aProtoEditChangesServedModule(t *testing.T) {
	parity.Covers(t, "R-NPM-010a")
	dir := harness.Fixture(t, "codegen")
	ecotest.WriteFile(t, filepath.Join(dir, "gensite", "bob.go"), siteGo)
	ecotest.WriteFile(t, filepath.Join(dir, "gensite", "index.html"), "<!doctype html><script type=\"module\" src=\"/src/main.ts\"></script>\n")
	ecotest.WriteFile(t, filepath.Join(dir, "gensite", "src", "main.ts"), "import { GreetResponse } from \"../gen/proto/greet/v1/greet_pb.js\";\nconsole.log(GreetResponse.typeName);\n")
	p, port, _, uid, gid, ok := devSession(t, dir, "gensite")
	if !ok {
		return
	}
	const module = "/gen/proto/greet/v1/greet_pb.ts"
	if _, err := waitGet(port, module, regexp.MustCompile(`GreetResponse`), 5*time.Second); err != nil {
		t.Fatalf("the generated module %s is not served: %v\n%s", module, err, p.output())
	}
	proto := filepath.Join(dir, "proto", "greet", "v1", "greet.proto")
	ecotest.ReplaceIn(t, proto, "string greeting = 1;", "string greeting = 1;\n  string farewell_marker = 3;")
	_ = os.Chown(proto, int(uid), int(gid))
	if _, err := waitGet(port, module, regexp.MustCompile(`farewellMarker`), 10*time.Second); err != nil {
		t.Errorf("the served module did not pick up the new proto field within 10 s: %v\n%s", err, p.output())
	}
}
