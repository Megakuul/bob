package remote

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/megakuul/bob/pkg/engine"
)

func envelope(flags byte, v any) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 5, 5+len(b))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:], uint32(len(b)))
	return append(out, b...)
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func untar(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Typeflag == tar.TypeSymlink {
			out[h.Name] = "-> " + h.Linkname
			continue
		}
		out[h.Name] = string(b)
	}
}

// TestPackSource: the snapshot holds the repository's files without .bob/ and .git/, is
// deterministic (equal trees, equal hash), and refuses symlinks that leave the repository.
func TestPackSource(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PATH", "") // no git: every file is taken
	dir := writeRepo(t, map[string]string{"bob.lock": "{}", "a/b.go": "package a", ".bob/state": "x", ".git/HEAD": "ref"})
	if err := os.Symlink("b.go", filepath.Join(dir, "a", "link")); err != nil {
		t.Fatal(err)
	}
	s1, err := PackSource(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Remove()
	got := untar(t, s1.Path)
	want := map[string]string{"bob.lock": "{}", "a/b.go": "package a", "a/link": "-> b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	b, _ := os.ReadFile(s1.Path)
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != s1.SHA256 || int64(len(b)) != s1.Size || s1.Entries != 3 {
		t.Errorf("hash/size/entries do not describe the file: %+v", s1)
	}
	// Same content, later mtimes: same bytes.
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "a", "b.go"), later, later)
	s2, err := PackSource(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Remove()
	if s1.SHA256 != s2.SHA256 {
		t.Errorf("snapshot is not deterministic: %s vs %s", s1.SHA256, s2.SHA256)
	}
	if err := os.Symlink("../../etc/passwd", filepath.Join(dir, "a", "evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := PackSource(ctx, dir); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Errorf("escaping symlink: err = %v, want an unsafe-path error", err)
	}
}

func TestMatchLabel(t *testing.T) {
	for _, c := range []struct {
		pats  []string
		label string
		want  bool
	}{
		{[]string{"//..."}, "//a/b:c", true},
		{[]string{"//a/..."}, "//a/b:c", true},
		{[]string{"//a/..."}, "//a:a", true},
		{[]string{"//a/..."}, "//ab:c", false},
		{[]string{"//a/b:c"}, "//a/b:c", true},
		{[]string{"//a/b:c@linux-amd64"}, "//a/b:c", true},
		{[]string{"//a/b"}, "//a/b:b", true},
		{[]string{"//a/b"}, "//a/b:c", false},
		{nil, "//x:y", true},
	} {
		if got := MatchLabel(c.pats, c.label); got != c.want {
			t.Errorf("MatchLabel(%v, %s) = %v, want %v", c.pats, c.label, got, c.want)
		}
	}
}

// TestErrors: Connect error bodies and transport failures are classified for the BOB-E mapping.
func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/x.S/Denied":
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"code":"permission_denied","message":"no"}`)
		case "/api/x.S/Plain":
			w.WriteHeader(401)
		default:
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	s := &Server{URL: srv.URL}
	ctx := context.Background()
	err := s.Call(ctx, "x.S/Denied", struct{}{}, nil)
	if e, ok := AsError(err); !ok || e.Code != "permission_denied" || e.Message != "no" || e.Unreachable() {
		t.Errorf("Denied: %#v", err)
	}
	if e, _ := AsError(s.Call(ctx, "x.S/Plain", struct{}{}, nil)); e == nil || e.Code != "unauthenticated" {
		t.Errorf("Plain 401: %#v", e)
	}
	if e, _ := AsError(s.Call(ctx, "x.S/Busy", struct{}{}, nil)); e == nil || !e.Unreachable() {
		t.Errorf("503: %#v", e)
	}
	dead := &Server{URL: "http://127.0.0.1:1"}
	if e, _ := AsError(dead.Call(ctx, "x.S/Any", struct{}{}, nil)); e == nil || !e.Unreachable() || e.Transport == nil {
		t.Errorf("no server: %#v", e)
	}
}

// fakeBuildServer serves the upload, CreateBuild and WatchBuild of one build.
type fakeBuildServer struct {
	mu       sync.Mutex
	uploads  map[string][]byte
	puts     int
	created  map[string]any
	watchErr bool // first WatchBuild ends with unavailable (orchestrator restart)
	watches  int
}

func (f *fakeBuildServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/source/sha256/"):
		f.puts++
		hexsum := strings.TrimPrefix(r.URL.Path, "/upload/source/sha256/")
		if _, ok := f.uploads[hexsum]; ok {
			w.WriteHeader(200) // existing: answered before the body (Expect: 100-continue)
			return
		}
		if r.Header.Get("Bob-Project") != "demo" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != hexsum {
			http.Error(w, "upload does not match its sha256", 400)
			return
		}
		f.uploads[hexsum] = b
		w.WriteHeader(201)
	case r.URL.Path == "/api/"+CreateBuildMethod:
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		_ = json.NewEncoder(w).Encode(map[string]any{"build": map[string]any{"id": "b-01TEST00000", "state": "BUILD_STATE_PENDING"}})
	case r.URL.Path == "/api/"+WatchBuildMethod:
		f.watches++
		w.Header().Set("Content-Type", "application/connect+json")
		if f.watchErr && f.watches == 1 {
			_, _ = w.Write(envelope(0, map[string]any{"build": map[string]any{"id": "b-01TEST00000", "state": "BUILD_STATE_RUNNING"},
				"changedNodes": []int{0}, "nodes": []any{map[string]any{"package": map[string]string{"label": "//x:y"}, "state": "PACKAGE_STATE_BUILDING"}}}))
			_, _ = w.Write(envelope(2, map[string]any{"error": map[string]string{"code": "unavailable", "message": "orchestrator stopping"}}))
			return
		}
		_, _ = w.Write(envelope(0, map[string]any{"build": map[string]any{"id": "b-01TEST00000", "state": "BUILD_STATE_RUNNING"},
			"changedNodes": []int{0, 1}, "nodes": []any{
				map[string]any{"package": map[string]string{"label": "//x:y"}, "state": "PACKAGE_STATE_BUILDING"},
				map[string]any{"package": map[string]string{"label": "//x:dep"}, "state": "PACKAGE_STATE_CACHED", "outputs": []string{"/nix/store/aaa-dep"}}}}))
		_, _ = w.Write(envelope(0, map[string]any{"build": map[string]any{"id": "b-01TEST00000", "state": "BUILD_STATE_SUCCEEDED"},
			"changedNodes": []int{0}, "nodes": []any{map[string]any{"package": map[string]string{"label": "//x:y"},
				"state": "PACKAGE_STATE_SUCCEEDED", "outputs": []string{"/nix/store/bbb-y"}}}}))
		_, _ = w.Write(envelope(2, map[string]any{}))
	default:
		w.WriteHeader(404)
	}
}

// TestExecutor: a remote build uploads its snapshot once, creates the build with the request,
// follows it across a broken watch stream, and fetches only the requested package's outputs.
func TestExecutor(t *testing.T) {
	t.Setenv("PATH", "")
	f := &fakeBuildServer{uploads: map[string][]byte{}, watchErr: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := writeRepo(t, map[string]string{"bob.lock": "{}", "x/bob.go": "package x"})
	var fetched []string
	x := &Executor{Server: &Server{URL: srv.URL, Token: "tok"}, Project: "demo",
		Fetch: func(_ context.Context, p []string) error { fetched = p; return nil }}
	run := func() []engine.Event {
		ch, err := x.Build(context.Background(), engine.RemoteRequest{Root: dir, Patterns: []string{"//x:y"}, Test: true})
		if err != nil {
			t.Fatal(err)
		}
		var evs []engine.Event
		for ev := range ch {
			evs = append(evs, ev)
		}
		return evs
	}
	evs := run()
	var names []string
	for _, ev := range evs {
		names = append(names, ev.Name)
	}
	if names[0] != EvSubmitted || names[len(names)-1] != EvDone {
		t.Fatalf("events %v: want remote.submitted first and remote.done last", names)
	}
	var done Done
	_ = json.Unmarshal(evs[len(evs)-1].Data, &done)
	if done.State != "succeeded" || done.Failure != nil || !reflect.DeepEqual(done.Downloaded, []string{"/nix/store/bbb-y"}) {
		t.Errorf("done = %+v", done)
	}
	if !reflect.DeepEqual(fetched, []string{"/nix/store/bbb-y"}) {
		t.Errorf("fetched %v, want only the requested package's output", fetched)
	}
	if f.watches != 2 {
		t.Errorf("WatchBuild called %d times, want a reconnect after the broken stream", f.watches)
	}
	src, _ := f.created["source"].(map[string]any)
	if f.created["projectId"] != "demo" || f.created["test"] != true || src["uploadSha256"] == "" || len(f.uploads) != 1 {
		t.Errorf("CreateBuild request %v, uploads %d", f.created, len(f.uploads))
	}
	// Second run: the server has the snapshot; the upload is answered without its body.
	f.watchErr = false
	run()
	if len(f.uploads) != 1 || f.puts != 2 {
		t.Errorf("uploads %d, PUTs %d", len(f.uploads), f.puts)
	}
}

// TestPKCE: the authorize URL carries an S256 challenge of the verifier sent to the token
// endpoint, and the redirect must come back to 127.0.0.1 with the right state.
func TestPKCE(t *testing.T) {
	var challenge, verifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/authorize":
			q := r.URL.Query()
			challenge = q.Get("code_challenge")
			http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+q.Get("state"), http.StatusFound)
		case "/oauth/token":
			_ = r.ParseForm()
			verifier = r.PostForm.Get("code_verifier")
			_, _ = io.WriteString(w, `{"access_token":"bob_at_x","refresh_token":"bob_rt_x","expires_in":3600}`)
		}
	}))
	defer srv.Close()
	s := &Server{URL: srv.URL}
	tok, err := s.PKCE(context.Background(), func(u string) error {
		go func() {
			if r, err := http.Get(u); err == nil {
				r.Body.Close()
			}
		}()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if tok.AccessToken != "bob_at_x" || len(verifier) < 43 || challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Errorf("token %+v, verifier %q, challenge %q", tok, verifier, challenge)
	}
}
