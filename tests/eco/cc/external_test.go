package cc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

const zstdLabel = "//third_party/zstd:zstd"

// zstdMakefile mimics the part of zstd's lib/Makefile the fixture uses:
// make PREFIX=$out install -C lib builds libzstd.a and installs library, header and .pc file.
const zstdMakefile = `PREFIX ?= /usr/local
CC ?= cc
AR ?= ar

libzstd.a: zstd.o
	$(AR) rcs $@ zstd.o

zstd.o: zstd.c zstd.h
	$(CC) -O2 -fPIC -c zstd.c -o zstd.o

install: libzstd.a
	mkdir -p $(PREFIX)/lib/pkgconfig $(PREFIX)/include
	cp libzstd.a $(PREFIX)/lib/
	cp zstd.h $(PREFIX)/include/
	printf 'prefix=$(PREFIX)\nlibdir=$${prefix}/lib\nincludedir=$${prefix}/include\nName: libzstd\nDescription: stand-in for the harness\nVersion: 1.5.7\nLibs: -L$${libdir} -lzstd\nCflags: -I$${includedir}\n' > $(PREFIX)/lib/pkgconfig/libzstd.pc
`

// zstdArchive returns a deterministic zstd-1.5.7.tar.gz with the API the fixture app uses
// (ZSTD_compress, ZSTD_isError; the "compression" copies the input). variant changes the
// archive content so its NAR hash differs.
func zstdArchive(t *testing.T, variant string) []byte {
	t.Helper()
	files := []struct{ name, body string }{
		{"zstd-1.5.7/lib/Makefile", zstdMakefile},
		{"zstd-1.5.7/lib/zstd.h", "#ifndef ZSTD_H\n#define ZSTD_H\n#include <stddef.h>\nsize_t ZSTD_compress(void *dst, size_t cap, const void *src, size_t n, int level);\nunsigned ZSTD_isError(size_t code);\n#endif\n"},
		{"zstd-1.5.7/lib/zstd.c", "#include <string.h>\n#include \"zstd.h\"\n/* " + variant + " */\nsize_t ZSTD_compress(void *dst, size_t cap, const void *src, size_t n, int level) {\n    (void)level;\n    if (n > cap) return (size_t)-1;\n    memcpy(dst, src, n);\n    return n;\n}\nunsigned ZSTD_isError(size_t code) { return code == (size_t)-1; }\n"},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("harness: %v", err)
		}
		_, _ = tw.Write([]byte(f.body))
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	return buf.Bytes()
}

// archiveServer is the harness HTTP server for the zstd External; the served bytes can be
// replaced while it runs.
type archiveServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body []byte
}

func newArchiveServer(t *testing.T, variant string) *archiveServer {
	t.Helper()
	a := &archiveServer{body: zstdArchive(t, variant)}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		b := a.body
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(b)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *archiveServer) serve(b []byte) {
	a.mu.Lock()
	a.body = b
	a.mu.Unlock()
}

func (a *archiveServer) url() string { return a.srv.URL + "/zstd-1.5.7.tar.gz" }

const upstreamURL = "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz"

// ccFixture copies the cc fixture with the zstd External pointed at the harness server, so the
// run needs no network, and returns the directory.
func ccFixture(t *testing.T, srv *archiveServer) string {
	t.Helper()
	dir := harness.Fixture(t, "cc")
	replaceIn(t, filepath.Join(dir, "third_party", "zstd", "bob.go"), upstreamURL, srv.url())
	return dir
}

// lockedCC is ccFixture after `bob lock` (against the served archive).
func lockedCC(t *testing.T, srv *archiveServer, home string) (string, bool) {
	t.Helper()
	dir := ccFixture(t, srv)
	if r := bobIn(t, dir, home, "lock"); !ok(t, r) {
		return dir, false
	}
	return dir, true
}

// ccExternal is the cc.externals entry of bob.lock.
type ccExternal struct {
	URL     string `json:"url"`
	Strip   int    `json:"strip"`
	NarHash string `json:"narHash"`
}

func readCCLock(t *testing.T, dir string) map[string]ccExternal {
	t.Helper()
	var l struct {
		CC struct {
			Externals map[string]ccExternal `json:"externals"`
		} `json:"cc"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "bob.lock"))), &l); err != nil {
		t.Fatalf("bob.lock: %v", err)
	}
	return l.CC.Externals
}

var sriRe = regexp.MustCompile(`sha256-[A-Za-z0-9+/]{43}=`)

// R-CC-005a: cc.External sources are pinned and verified before building: (a) a bob.lock
// without the entry makes bob build exit 3 with BOB-E-1009 before Nix runs; (b) replaced
// archive bytes make the build exit 4 with E-FETCH-HASH (BOB-E-3001) naming label, URL,
// expected and actual hash; (c) unchanged, a C consumer linking Zstd(p) builds and runs under
// env -i. The URL of the External points at the harness HTTP server (the real
// github.com archive is not fetched).
func TestRCC005aExternalPinnedAndVerified(t *testing.T) {
	parity.Covers(t, "R-CC-005a")
	srv := newArchiveServer(t, "original")
	dir, locked := lockedCC(t, srv, t.TempDir())
	if !locked {
		return
	}
	pinned := readCCLock(t, dir)[zstdLabel]
	if pinned.URL != srv.url() || pinned.Strip != 1 || !sriRe.MatchString(pinned.NarHash) {
		t.Fatalf("bob lock did not pin %s as {url, strip 1, narHash}: %+v", zstdLabel, pinned)
	}
	lockBytes := readFile(t, filepath.Join(dir, "bob.lock"))

	// (a) entry removed.
	var raw map[string]any
	if err := json.Unmarshal([]byte(lockBytes), &raw); err != nil {
		t.Fatalf("bob.lock: %v", err)
	}
	ccSection, _ := raw["cc"].(map[string]any)
	ext, _ := ccSection["externals"].(map[string]any)
	delete(ext, zstdLabel)
	stripped, _ := json.MarshalIndent(raw, "", "  ")
	writeFile(t, filepath.Join(dir, "bob.lock"), string(stripped)+"\n")
	r := bobIn(t, dir, t.TempDir(), "build", "//...", "--json")
	if exitIs(t, r, 3) {
		if e, found := errorWithCode(t, r, "BOB-E-1009"); found && !strings.Contains(e.Message, zstdLabel) {
			t.Errorf("BOB-E-1009 does not name %s: %q", zstdLabel, e.Message)
		}
		for _, ev := range eventsNamed(events(t, r), "package.start") {
			t.Errorf("a package started (%v) although the missing pin must stop bob before Nix runs", ev)
		}
	}
	writeFile(t, filepath.Join(dir, "bob.lock"), lockBytes)

	// (c) unchanged: the consumer builds and runs under env -i.
	home := t.TempDir()
	paths, built := outputPaths(t, dir, home, "//app:app")
	if built {
		app := paths["//app:app"]
		if len(app) == 0 {
			t.Fatalf("no output path for //app:app: %v", paths)
		}
		// --print-paths gives the host path <home>/nix/store/…; store binaries run only through
		// bob run/bob exec (SPEC §02.6), where /nix/store is bob's store. env -i keeps the empty
		// environment, so the External's library is found through the RUNPATH alone.
		// bob exec needs the project's .bob/env.json (BOB-E-7006 otherwise, R-DEV-008): bob env first.
		inNS := filepath.Join("/nix/store", filepath.Base(app[0]), "bin", "app")
		run := bobIn(t, dir, home, "env", "--format", "json")
		if run.Exit == 0 {
			run = bobIn(t, dir, home, "exec", "--", "env", "-i", inNS)
		}
		if run.Exit != 0 || !strings.HasPrefix(run.Stdout, "gcd=12 ") || !strings.Contains(run.Stdout, "zstd=12->12") {
			t.Errorf("app under env -i exited %d and printed %q; want the gcd=12 line linking the External", run.Exit, run.Stdout+run.Stderr)
		}
	}

	// (b) replaced bytes, fresh store: the fetcher refuses the content.
	srv.serve(zstdArchive(t, "tampered"))
	r = bobIn(t, dir, t.TempDir(), "build", "//app:app", "--json")
	if !exitIs(t, r, 4) {
		return
	}
	e, found := errorWithCode(t, r, "BOB-E-3001")
	if !found {
		return
	}
	text := strings.Join(append([]string{e.Message, e.Label}, e.Excerpt...), "\n")
	for _, want := range []string{zstdLabel, srv.url(), pinned.NarHash} {
		if !strings.Contains(text, want) {
			t.Errorf("E-FETCH-HASH does not contain %q:\n%s", want, text)
		}
	}
	if actual := sriRe.FindAllString(strings.ReplaceAll(text, pinned.NarHash, ""), -1); len(actual) == 0 {
		t.Errorf("E-FETCH-HASH does not show the actual hash:\n%s", text)
	}
}
