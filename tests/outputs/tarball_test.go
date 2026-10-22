package outputs

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// R-OUT-004: a Static tarball of the pure-Go stamp binary holds exactly bin/stamp and
// bob/licences.json and is byte-identical across builds; a cgo binary as Static is rejected; a
// Closure tarball has the closure under nix/store, bob/roots and the R-OUT-010 tar rules.
func TestROUT004Tarballs(t *testing.T) {
	parity.Covers(t, "R-OUT-004")
	goW := newWorld(t, "go")
	goW.overlay(map[string]string{"tools/tar/bob.go": stamptarGo})
	out, ok := goW.buildOne("//tools/tar:stamp")
	if !ok {
		return
	}
	tgz := onlyFile(t, out, "stamp-*.tar.gz")
	if want := "stamp-" + string(hostPlatform(t)) + ".tar.gz"; filepath.Base(tgz) != want {
		t.Errorf("tarball file is %s, want %s", filepath.Base(tgz), want)
	}
	raw := readFile(t, tgz)
	gzipHeaderOK(t, "static tarball", raw)
	es := readTar(t, raw)
	checkTarRules(t, "static tarball", es)
	var files []string
	for _, e := range es {
		if e.Hdr.Typeflag != '5' {
			files = append(files, strings.TrimPrefix(e.Hdr.Name, "./"))
		}
	}
	if got := fmtSet(files); got != `["bin/stamp" "bob/licences.json"]` {
		t.Errorf("static tarball contains %s, want exactly bin/stamp and bob/licences.json", got)
	}
	if e, ok := tarFind(es, "bin/stamp"); !ok || e.Hdr.Mode&0o111 == 0 {
		t.Errorf("bin/stamp is missing or not executable")
	}
	again, ok := goW.freshStore().buildOne("//tools/tar:stamp")
	if !ok {
		return
	}
	if a, b := sha256File(t, tgz), sha256File(t, onlyFile(t, again, "stamp-*.tar.gz")); a != b {
		t.Errorf("two builds of the static tarball differ: %s vs %s", a, b)
	}

	w := newWorld(t, "polyglot")
	w.overlay(map[string]string{"tartest/bob.go": tartestGo})
	r := w.bob("build", "//tartest:api-static")
	if harness.Red(t, r) {
		return
	}
	re := regexp.MustCompile(`✗ //cmd/api:api is not a static binary \(it needs glibc-[0-9][0-9.]*, openssl-[0-9][0-9.]*\); use tarball\.Closure`)
	if r.Exit != 3 || !re.MatchString(r.Stderr) {
		t.Errorf("static tarball of a cgo binary: exit %d, want 3 and `✗ //cmd/api:api is not a static binary (it needs glibc-…, openssl-…); use tarball.Closure`\n%s", r.Exit, r.Stderr)
	}

	cout, ok := w.buildOne("//deploy:api-tar")
	if !ok {
		return
	}
	craw := readFile(t, onlyFile(t, cout, "api-tar-*.tar.gz"))
	gzipHeaderOK(t, "closure tarball", craw)
	ces := readTar(t, craw)
	checkTarRules(t, "closure tarball", ces)
	if e, ok := tarFind(ces, "bob/roots"); !ok || !strings.Contains(string(e.Data), "/nix/store/") {
		t.Errorf("closure tarball has no bob/roots listing the roots")
	}
	if _, ok := tarFind(ces, "bob/licences.json"); !ok {
		t.Errorf("closure tarball has no bob/licences.json")
	}
	if len(storeTops(ces)) < 2 {
		t.Errorf("closure tarball holds %v, want the whole closure under nix/store", storeTops(ces))
	}
}
