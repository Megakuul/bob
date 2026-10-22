package devenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// tree records every file below the roots (path → size, mode, content hash) so that two
// snapshots can be diffed. Unreadable entries are recorded as such.
func tree(roots ...string) map[string]string {
	m := map[string]string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				m[p] = "unreadable"
				return nil
			}
			info, err := d.Info()
			if err != nil {
				m[p] = "unreadable"
				return nil
			}
			switch {
			case d.IsDir():
				m[p] = "dir"
			case info.Mode().IsRegular():
				f, err := os.Open(p)
				if err != nil {
					m[p] = "unreadable"
					return nil
				}
				h := sha256.New()
				_, _ = io.Copy(h, f)
				f.Close()
				m[p] = fmt.Sprintf("file %o %s", info.Mode().Perm(), hex.EncodeToString(h.Sum(nil))[:16])
			default:
				m[p] = "other " + info.Mode().String()
			}
			return nil
		})
	}
	return m
}

// changed lists the paths that differ between two snapshots.
func changed(before, after map[string]string) []string {
	var out []string
	for p, v := range after {
		if before[p] != v {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// confValues returns the space-separated values of a "key = a b c" line of a nix.conf.
func confValues(conf, key string) []string {
	for _, l := range strings.Split(conf, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.Fields(v)
		}
	}
	return nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// TestRDEV004CacheSetupAfterLogin: bob login writes bob's own nix.conf and netrc and touches
// nothing outside bob's state and config directories; bob logout removes the three entries.
func TestRDEV004CacheSetupAfterLogin(t *testing.T) {
	parity.Covers(t, "R-DEV-004")
	srv := fakeserver.New(t, fakeserver.Options{})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				for _, c := range srv.DeviceCodes() {
					srv.ApproveDevice(c, "linus")
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })

	w := newWorld(t)
	dir := w.scratch()
	conf := filepath.Join(w.stateHome(), "etc", "nix", "nix.conf")
	netrc := filepath.Join(w.stateHome(), "etc", "nix", "netrc")
	bobDirs := []string{
		filepath.Join(w.home, ".config", "bob"), filepath.Join(w.home, ".local", "share", "bob"),
		filepath.Join(w.home, ".local", "state", "bob"), filepath.Join(w.home, ".cache", "bob"),
	}
	homeBefore, etcBefore := tree(w.home), tree("/etc")

	r := w.bob(dir, "login", "--device", srv.URL())
	if redIf(t, r) || !exitIs(t, r, 0) {
		return
	}
	noLeak(t, r)
	srvURL, _ := url.Parse(srv.URL())
	cache := srv.URL() + "/nix"

	text := readFile(t, conf)
	subs := confValues(text, "substituters")
	ci, si := indexOf(subs, "https://cache.nixos.org"), indexOf(subs, cache)
	if ci < 0 || si < 0 || si < ci {
		t.Errorf("substituters = %v, want %s after https://cache.nixos.org", subs, cache)
	}
	keys := confValues(text, "trusted-public-keys")
	if indexOf(keys, srv.PublicKey()) < 0 {
		t.Errorf("trusted-public-keys = %v, lacks the server's key %s (nix.public_keys of AuthService/WhoAmI)", keys, srv.PublicKey())
	}
	if indexOf(keys, "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=") < 0 {
		t.Errorf("trusted-public-keys = %v, lost the key of cache.nixos.org", keys)
	}
	var creds struct {
		Servers map[string]struct {
			AccessToken string `json:"accessToken"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(w.home, ".config", "bob", "credentials.json"))), &creds); err != nil {
		t.Fatalf("credentials.json: %v", err)
	}
	token := creds.Servers[srv.URL()].AccessToken
	if token == "" {
		t.Fatalf("credentials.json has no token for %s", srv.URL())
	}
	st, err := os.Stat(netrc)
	if err != nil {
		t.Fatalf("netrc: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("netrc mode %v, want 0600", st.Mode().Perm())
	}
	want := "machine " + srvURL.Hostname() + " login bob password " + token
	if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(want) + `\s*$`).MatchString(readFile(t, netrc)) {
		t.Errorf("netrc lacks the line %q:\n%s", want, readFile(t, netrc))
	}

	// Nothing outside bob's directories changed.
	for _, p := range changed(homeBefore, tree(w.home)) {
		inside := false
		for _, d := range bobDirs {
			if p == d || strings.HasPrefix(p, d+"/") {
				inside = true
			}
		}
		if !inside {
			t.Errorf("bob login changed %s, outside bob's state and config directories", p)
		}
	}
	if d := changed(etcBefore, tree("/etc")); len(d) > 0 {
		t.Errorf("bob login changed files in /etc: %v", d)
	}

	// Logout removes the three entries.
	r = w.bob(dir, "logout", srv.URL())
	if redIf(t, r) || !exitIs(t, r, 0) {
		return
	}
	if b, err := os.ReadFile(conf); err == nil {
		after := string(b)
		if indexOf(confValues(after, "substituters"), cache) >= 0 {
			t.Errorf("after logout substituters still lists %s", cache)
		}
		if indexOf(confValues(after, "trusted-public-keys"), srv.PublicKey()) >= 0 {
			t.Errorf("after logout trusted-public-keys still has the server key")
		}
	}
	if b, err := os.ReadFile(netrc); err == nil && strings.Contains(string(b), "machine "+srvURL.Hostname()) {
		t.Errorf("after logout the netrc still has an entry for %s:\n%s", srvURL.Hostname(), b)
	}
}

// stackInfo starts the all-in-one stack (HARNESS §13.2) and returns its API URL and admin token;
// while the server is a stub the info line lacks them and the test is red.
func stackInfo(t *testing.T) (api, token, stackDir string, ok bool) {
	t.Helper()
	if r := harness.RunPath(t, harness.Bin(t, "bob-server"), harness.Spec{Timeout: time.Minute}, "orchestrator"); r.IsNotImplemented() {
		harness.RedFeature(t, "bob-server orchestrator: "+strings.TrimSpace(strings.SplitN(r.Combined(), "\n", 2)[0]))
		return "", "", "", false
	}
	root := harness.Root(t)
	bin := filepath.Join(t.TempDir(), "devstack")
	build := exec.Command("go", "build", "-o", bin, "./tools/devstack")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("harness error: go build devstack: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	stackDir = t.TempDir()
	cmd := exec.CommandContext(ctx, bin, "up", "-dir", stackDir, "-json")
	cmd.Dir = root
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness error: devstack: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		fin := make(chan struct{})
		go func() { _ = cmd.Wait(); close(fin) }()
		select {
		case <-fin:
		case <-time.After(30 * time.Second):
			cancel()
			<-fin
		}
		cancel()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("harness error: devstack printed no info line: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("harness error: devstack info line: %v: %q", err, line)
	}
	api, _ = m["api_url"].(string)
	token, _ = m["admin_token"].(string)
	if api == "" || token == "" {
		harness.RedFeature(t, "bob-server orchestrator (the devstack info line has no api_url/admin_token)")
		return "", "", "", false
	}
	return api, token, stackDir, true
}

// rpc calls a ConnectRPC method with the JSON codec and returns the status and body.
func rpc(api, token, method string, req any) (int, []byte, error) {
	b, _ := json.Marshal(req)
	r, err := http.NewRequest(http.MethodPost, api+"/"+method, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: time.Minute}).Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, nil
}

// developerToken provisions a developer: a role that may use the build service and the cache
// downloads, a service account bound to it, and an API token (CreateServiceAccountToken).
func developerToken(t *testing.T, api, admin string) string {
	t.Helper()
	must := func(method string, req any) []byte {
		code, body, err := rpc(api, admin, method, req)
		if err != nil || code != 200 {
			t.Fatalf("harness error: %s: %d %v %s", method, code, err, body)
		}
		return body
	}
	must("bob.access.v1.RoleService/CreateRole", map[string]any{"role": map[string]any{
		"id": "developer",
		"permissions": []map[string]string{
			{"method": "bob.build.v1.BuildService/*", "project": "*"},
			{"method": "bob.log.v1.LogService/*", "project": "*"},
			{"method": "bob.cache.v1.CacheService/GetPath", "project": "*"},
			{"method": "bob.auth.v1.AuthService/WhoAmI", "project": "*"},
		}}})
	must("bob.access.v1.ServiceAccountService/CreateServiceAccount", map[string]any{"serviceAccount": map[string]any{"id": "dev-1"}})
	must("bob.access.v1.RoleService/CreateRoleBinding", map[string]any{"principalId": "sa:dev-1", "role": "developer", "project": "*"})
	var tok struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(must("bob.access.v1.ServiceAccountService/CreateServiceAccountToken",
		map[string]any{"serviceAccountId": "dev-1", "name": "t", "scope": "TOKEN_SCOPE_API", "ttlDays": 1}), &tok); err != nil || tok.Secret == "" {
		t.Fatalf("harness error: no token secret: %v", err)
	}
	return tok.Secret
}

// TestRDEV005RemoteBuildsInsteadOfPushes: a developer token cannot write the cache (PUT /nix/*
// and the worker-internal promote RPCs answer 403), bob build --remote writes nothing to the
// cache endpoints and leaves valid, server-signed outputs in the local store, and Ctrl-C detaches
// without cancelling (bob cancel does).
func TestRDEV005RemoteBuildsInsteadOfPushes(t *testing.T) {
	parity.Covers(t, "R-DEV-005")
	api, admin, stackDir, ok := stackInfo(t)
	if !ok {
		return
	}
	dev := developerToken(t, api, admin)

	// The permission side: 403 on the cache write path and on the internal promote service.
	req, _ := http.NewRequest(http.MethodPut, api+"/nix/nar/0000000000000000000000000000000000000000000000000000.nar.zst", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+dev)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT /nix/nar/… with a developer token: status %d, want 403", resp.StatusCode)
	}
	if code, body, err := rpc(api, dev, "bob.worker.v1.WorkerControlService/GetCheckoutCredential", map[string]any{}); err != nil || code != http.StatusForbidden {
		t.Errorf("worker-internal RPC with a developer token: status %d (%v) %s, want 403", code, err, body)
	}

	w := newWorld(t)
	dir := w.local("remote")
	if r := w.run(runOpts{dir: dir, stdin: dev}, bobBin(t), "login", "--token-stdin", api); redIf(t, r) || !exitIs(t, r, 0) {
		return
	}

	// bob build --remote: builds on the fleet, downloads, never writes the cache.
	logBefore := logSizes(stackDir)
	r := w.bob(dir, "build", "--remote", "--print-paths", "--json", "//x:quick")
	if redIf(t, r) || !exitIs(t, r, 0) {
		return
	}
	noLeak(t, r)
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		var ev struct {
			Event string   `json:"event"`
			Paths []string `json:"paths"`
		}
		if json.Unmarshal([]byte(l), &ev) == nil && ev.Event == "package.finish" {
			paths = append(paths, ev.Paths...)
		}
	}
	if len(paths) == 0 {
		t.Fatalf("bob build --remote --print-paths reported no output paths:\n%s", r.Stdout)
	}
	nixBin := pinned(t, "nix", "nixVersions.nix_2_34", "nix")
	for _, p := range paths {
		logical := "/nix/store/" + strings.TrimPrefix(p, w.stateHome()+"/nix/store/")
		out, err := exec.Command(nixBin, "--extra-experimental-features", "nix-command",
			"path-info", "--sigs", "--store", "local?root="+w.stateHome(), logical).CombinedOutput()
		if err != nil || !strings.Contains(string(out), ":") {
			t.Errorf("local store: %s is not valid or has no signature: %v\n%s", logical, err, out)
			continue
		}
		// The signature is the server's: its key name is the one WhoAmI announces.
		if want := serverKeyName(t, api, dev); want != "" && !strings.Contains(string(out), want+":") {
			t.Errorf("local store: %s is not signed by the server key %q:\n%s", logical, want, out)
		}
	}
	if m := cacheWrite.FindString(logsSince(stackDir, logBefore)); m != "" {
		t.Errorf("the server's request log has a write to a cache endpoint during bob build --remote: %q", m)
	}

	// Ctrl-C detaches and leaves the server build running; bob cancel stops it.
	p := startBob(t, w, dir, "build", "--remote", "//x:slow")
	m, found := p.waitOutput(regexp.MustCompile(`b-[0-9A-Z]{10,}`), 3*time.Minute)
	if !found {
		t.Fatalf("bob build --remote //x:slow printed no build id:\n%s", p.output())
	}
	time.Sleep(time.Second)
	_ = syscall.Kill(p.cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("bob did not exit after Ctrl-C")
	}
	if !strings.Contains(p.output(), "bob cancel "+m) {
		t.Errorf("after Ctrl-C bob does not print `bob cancel %s`:\n%s", m, p.output())
	}
	time.Sleep(3 * time.Second)
	if st := buildState(t, api, dev, m); st != "BUILD_STATE_RUNNING" {
		t.Errorf("3 s after Ctrl-C the server build %s is %s, want BUILD_STATE_RUNNING", m, st)
	}
	if r := w.bob(dir, "cancel", m); !exitIs(t, r, 0) {
		return
	}
	var st string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if st = buildState(t, api, dev, m); st == "BUILD_STATE_CANCELLED" {
			return
		}
	}
	t.Errorf("after bob cancel the server build %s is %s, want BUILD_STATE_CANCELLED", m, st)
}

// cacheWrite matches an access-log line of a write to the cache endpoints.
var cacheWrite = regexp.MustCompile(`(?m)^.*\b(PUT|POST|DELETE)\b[^\n]{0,120}/nix/.*$`)

// logSizes records the size of every stack process log, so that logsSince returns only what was
// written afterwards (per file: the logs of several processes are not one append-only stream).
func logSizes(dir string) map[string]int64 {
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "*.log"))
	m := map[string]int64{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			m[f] = st.Size()
		}
	}
	return m
}

// logsSince is the concatenated text written to every stack process log (the server's request
// log) after the sizes were taken by logSizes; files created since count completely.
func logsSince(dir string, before map[string]int64) string {
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "*.log"))
	var b strings.Builder
	for _, f := range files {
		c, _ := os.ReadFile(f)
		if off := before[f]; off <= int64(len(c)) {
			c = c[off:]
		}
		b.Write(c)
		b.WriteByte('\n')
	}
	return b.String()
}

// serverKeyName returns the key name of the server's cache key as announced by WhoAmI
// (nix.public_keys, "name:base64").
func serverKeyName(t *testing.T, api, token string) string {
	t.Helper()
	code, body, err := rpc(api, token, "bob.auth.v1.AuthService/WhoAmI", map[string]any{})
	if err != nil || code != 200 {
		t.Fatalf("harness error: WhoAmI: %d %v %s", code, err, body)
	}
	var who struct {
		Nix struct {
			PublicKeys []string `json:"publicKeys"`
		} `json:"nix"`
	}
	if json.Unmarshal(body, &who) != nil || len(who.Nix.PublicKeys) == 0 {
		return ""
	}
	name, _, _ := strings.Cut(who.Nix.PublicKeys[0], ":")
	return name
}

// buildState returns the state name of a server build.
func buildState(t *testing.T, api, token, id string) string {
	t.Helper()
	code, body, err := rpc(api, token, "bob.build.v1.BuildService/GetBuild", map[string]any{"id": id})
	if err != nil || code != 200 {
		t.Fatalf("harness error: GetBuild %s: %d %v %s", id, code, err, body)
	}
	var b struct {
		Build struct {
			State string `json:"state"`
		} `json:"build"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Build.State
}
