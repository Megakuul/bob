package core

// SPEC §1.8 bob.lock and §03.0.3 / §03.7: R-FW-004, R-FW-005, R-FW-048, R-FW-062, R-FW-065.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// lockFixtures are all fixture repositories (R-FW-065: "for every fixture").
var lockFixtures = []string{"acme", "go", "rust", "npm", "ocaml", "cc", "polyglot", "codegen", "cpp-lib", "bigclosure", "nondet"}

// namesEcosystem reports whether msg names ecosystem eco as a word. A bare substring test would
// match "go" inside "bob.go" or "golang" in every message that cites a position.
func namesEcosystem(msg, eco string) bool {
	return regexp.MustCompile(`(^|[^.\w])` + regexp.QuoteMeta(eco) + `($|[^\w])`).MatchString(msg)
}

var sriRe = regexp.MustCompile(`^sha(256|512)-[A-Za-z0-9+/]+={0,2}$`)

// canonicalLock re-encodes lock bytes in the canonical form of R-FW-004.
func canonicalLock(t *testing.T, data []byte) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("bob.lock is not JSON: %v", err)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// checkCanonical fails t when data is not canonical bob.lock JSON or holds a malformed hash.
func checkCanonical(t *testing.T, data []byte) {
	t.Helper()
	if want := canonicalLock(t, data); !bytes.Equal(data, want) {
		t.Errorf("bob.lock is not canonical (2-space indent, sorted keys, one final newline):\n%s", data)
	}
	if bytes.Contains(data, []byte("\r")) || !bytes.HasSuffix(data, []byte("}\n")) || bytes.HasSuffix(data, []byte("\n\n")) {
		t.Errorf("bob.lock does not end with exactly one newline or uses CR line ends")
	}
	if regexp.MustCompile(`[0-9]\.[0-9]|[0-9][eE][+-]?[0-9]`).Match(regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).ReplaceAll(data, nil)) {
		t.Errorf("bob.lock contains a floating point number")
	}
	var walk func(k string, v any)
	walk = func(k string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for kk, vv := range x {
				walk(kk, vv)
			}
		case []any:
			for _, vv := range x {
				walk(k, vv)
			}
		case string:
			if (k == "narHash" || k == "checksum" || k == "integrity" || k == "tree") && !sriRe.MatchString(x) {
				t.Errorf("%s %q is not an SRI hash", k, x)
			}
		}
	}
	var v any
	_ = json.Unmarshal(data, &v)
	walk("", v)
}

// sections splits a bob.lock into its top-level members (raw JSON).
func sections(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("bob.lock is not JSON: %v\n%s", err, data)
	}
	return m
}

// R-FW-004 (a): bob lock twice gives identical bytes, in canonical form.
func TestRFW004LockTwiceIdentical(t *testing.T) {
	parity.Covers(t, "R-FW-004", "R-FW-065")
	for _, name := range lockFixtures {
		t.Run(name, func(t *testing.T) {
			dir := harness.Fixture(t, name)
			if r := bobRun(t, dir, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
				return
			}
			first := readFile(t, filepath.Join(dir, "bob.lock"))
			checkCanonical(t, []byte(first))
			if r := bobRun(t, dir, "lock"); !exitIs(t, r, 0) {
				return
			}
			if second := readFile(t, filepath.Join(dir, "bob.lock")); second != first {
				t.Errorf("bob lock twice changed bob.lock")
			}
			// Every section is a hash of its declarations (R-FW-062).
			for k, raw := range sections(t, []byte(first)) {
				switch k {
				case "go", "rust", "npm", "opam", "cc":
					var s struct {
						Inputs string `json:"inputs"`
					}
					if err := json.Unmarshal(raw, &s); err != nil || s.Inputs == "" {
						t.Errorf("section %s has no inputs: %v", k, err)
					}
				}
			}
		})
	}
}

// R-FW-004 (b): reformatting bob.lock by hand makes the next bob build rewrite it canonically
// and print "note: bob.lock was reformatted".
func TestRFW004ReformattedLockIsRewritten(t *testing.T) {
	parity.Covers(t, "R-FW-004")
	dir := harness.Fixture(t, "acme")
	if r := bobRun(t, dir, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	path := filepath.Join(dir, "bob.lock")
	canon := readFile(t, path)
	var v any
	if err := json.Unmarshal([]byte(canon), &v); err != nil {
		t.Fatal(err)
	}
	four, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(four))
	r := bobRun(t, dir, "build", "//...")
	if !exitIs(t, r, 0) {
		return
	}
	if !strings.Contains(r.Stderr+r.Stdout, "note: bob.lock was reformatted") {
		t.Errorf("no \"note: bob.lock was reformatted\" in the output:\n%s%s", r.Stdout, r.Stderr)
	}
	if got := readFile(t, path); got != canon {
		t.Errorf("bob build did not rewrite bob.lock canonically")
	}
}

// R-FW-004 (c): lock.schema.json validates every example lock (the fixtures' and the ones bob
// lock writes), and describes the top-level members of the container.
func TestRFW004SchemaValidatesLocks(t *testing.T) {
	parity.Covers(t, "R-FW-004")
	sb, err := os.ReadFile(filepath.Join(harness.Root(t), "pkg", "engine", "lock", "lock.schema.json"))
	if err != nil {
		t.Fatalf("pkg/engine/lock/lock.schema.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(sb, &schema); err != nil {
		t.Fatalf("lock.schema.json is not JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, k := range []string{"lockVersion", "nixpkgs", "sources", "go", "rust", "npm", "opam", "cc", "nixpkgsFacts"} {
		if _, ok := props[k]; !ok {
			t.Errorf("lock.schema.json has no property %q", k)
		}
	}
	check := func(name string, data []byte) {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		for _, e := range validate(schema, v, "$") {
			t.Errorf("%s: %s", name, e)
		}
	}
	for _, name := range lockFixtures {
		check("fixture "+name, []byte(readFile(t, filepath.Join(harness.Root(t), "tests", "fixtures", "repos", name, "bob.lock"))))
	}
	dir := harness.Fixture(t, "polyglot")
	if r := bobRun(t, dir, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	check("polyglot after bob lock", []byte(readFile(t, filepath.Join(dir, "bob.lock"))))
}

// R-FW-004 unit: lock.File.Write is canonical, atomic and only touches the file when the bytes
// change; Read reads it back.
func TestRFW004FileWriteIsAtomicAndStable(t *testing.T) {
	parity.Covers(t, "R-FW-004")
	dir := t.TempDir()
	path := filepath.Join(dir, "bob.lock")
	f := &lock.File{LockVersion: 1, Sections: map[string]json.RawMessage{"go": json.RawMessage(`{"modules":[],"inputs":"` + strings.Repeat("0", 64) + `"}`)}}
	err := f.Write(path)
	if isNotImplemented(err) {
		harness.RedFeature(t, "lock.File.Write")
		return
	}
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	first := readFile(t, path)
	checkCanonical(t, []byte(first))
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(path); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || !st.ModTime().Equal(old) {
		t.Errorf("Write rewrote an unchanged bob.lock (mtime %v)", st.ModTime())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("Write left temporary files behind: %v", names)
	}
	back, err := lock.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if back.LockVersion != 1 || len(back.Sections["go"]) == 0 {
		t.Errorf("Read returned %+v", back)
	}
}

// R-FW-005 (a): adding a requirement and running bob lock changes only that ecosystem's
// section; bob build before bob lock exits with the stale-lock error and makes no network
// request.
func TestRFW005LockOnDemand(t *testing.T) {
	parity.Covers(t, "R-FW-005", "R-FW-062")
	dir := harness.Fixture(t, "acme")
	bobHome := t.TempDir()
	env := []string{"BOB_HOME=" + bobHome}
	if r := bobRunEnv(t, dir, env, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	before := sections(t, []byte(readFile(t, filepath.Join(dir, "bob.lock"))))
	bobgo := filepath.Join(dir, "bob.go")
	replaceIn(t, bobgo, `golang.Require("github.com/google/uuid", "v1.6.0")`,
		`golang.Require("github.com/google/uuid", "v1.6.0"), golang.Require("golang.org/x/text", "v0.20.0")`)
	line := 0
	for i, l := range strings.Split(readFile(t, bobgo), "\n") {
		if strings.Contains(l, "golang.org/x/text") {
			line = i + 1
		}
	}
	// bob build before bob lock: stale-lock error, no network.
	r := bobNoNetworkHome(t, dir, bobHome, "build", "//...", "--json")
	if !exitIs(t, r, 3) {
		return
	}
	e, ok := errorWithCode(t, r, "BOB-E-1012")
	if ok {
		if !namesEcosystem(e.Message, "go") {
			t.Errorf("the stale-lock error does not name the go ecosystem: %q", e.Message)
		}
		if !regexp.MustCompile(fmt.Sprintf(`bob\.go:%d\b`, line)).MatchString(e.DefinedAt + " " + e.Location + " " + e.Message) {
			t.Errorf("the stale-lock error does not name bob.go:%d: %+v", line, e)
		}
		if e.Hint != "run bob lock" {
			t.Errorf("hint %q, want \"run bob lock\"", e.Hint)
		}
	}
	if r := bobRunEnv(t, dir, env, "lock"); !exitIs(t, r, 0) {
		return
	}
	after := sections(t, []byte(readFile(t, filepath.Join(dir, "bob.lock"))))
	var changed []string
	for k := range after {
		if !bytes.Equal(after[k], before[k]) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	if len(changed) != 1 || changed[0] != "go" {
		t.Errorf("bob lock after adding a Go requirement changed sections %v, want [go] only", changed)
	}
	// A second bob lock without changes makes no registry request (warm caches, no network)
	// and rewrites nothing; so does --refresh-less build.
	snap := treeHash(t, dir)
	if r := bobNoNetworkHome(t, dir, bobHome, "lock"); !exitIs(t, r, 0) {
		return
	}
	if treeHash(t, dir) != snap {
		t.Errorf("a second bob lock without changes rewrote files")
	}
}

// R-FW-005 (b): entries no declaration uses are removed by bob lock; pins move only with
// --update; --lock lets bob build re-resolve the stale section.
func TestRFW005RemovalAndPins(t *testing.T) {
	parity.Covers(t, "R-FW-005")
	dir := harness.Fixture(t, "acme")
	if r := bobRun(t, dir, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	lockPath := filepath.Join(dir, "bob.lock")
	withUUID := readFile(t, lockPath)
	if !strings.Contains(withUUID, "github.com/google/uuid") {
		t.Fatalf("bob.lock lacks github.com/google/uuid after bob lock:\n%s", withUUID)
	}
	replaceIn(t, filepath.Join(dir, "bob.go"), `Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},`, `Require: []golang.Requirement{},`)
	if r := bobRun(t, dir, "lock"); !exitIs(t, r, 0) {
		return
	}
	if strings.Contains(readFile(t, lockPath), "github.com/google/uuid") {
		t.Errorf("bob lock kept github.com/google/uuid although no declaration uses it")
	}
	// bob build --lock re-resolves a stale section instead of failing.
	replaceIn(t, filepath.Join(dir, "bob.go"), `Require: []golang.Requirement{},`, `Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},`)
	if r := bobRun(t, dir, "build", "//internal/greet/...", "--lock"); !exitIs(t, r, 0) {
		return
	}
	if !strings.Contains(readFile(t, lockPath), "github.com/google/uuid") {
		t.Errorf("bob build --lock did not re-resolve the stale go section")
	}
}

// R-FW-048: a bob.lock with merge-conflict markers fails with BOB-E-1019; bob lock --rebuild
// succeeds and keeps the nixpkgs revision.
func TestRFW048UnparseableLock(t *testing.T) {
	parity.Covers(t, "R-FW-048")
	dir := harness.Fixture(t, "acme")
	if r := bobRun(t, dir, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	path := filepath.Join(dir, "bob.lock")
	rev := sections(t, []byte(readFile(t, path)))["nixpkgs"]
	var pin lock.NixpkgsPin
	if err := json.Unmarshal(rev, &pin); err != nil || pin.Rev == "" {
		t.Fatalf("bob lock wrote no nixpkgs pin: %v %s", err, rev)
	}
	ls := strings.Split(readFile(t, path), "\n")
	conflicted := append([]string{ls[0], "<<<<<<< HEAD"}, ls[1:]...)
	conflicted = append(conflicted[:len(conflicted)-2], "=======", `  "lockVersion": 1`, ">>>>>>> other", conflicted[len(conflicted)-2], conflicted[len(conflicted)-1])
	writeFile(t, path, strings.Join(conflicted, "\n"))
	r := bobRun(t, dir, "build", "//...", "--json")
	if !exitIs(t, r, 3) {
		return
	}
	e, ok := errorWithCode(t, r, "BOB-E-1019")
	if ok {
		if !regexp.MustCompile(`^bob\.lock cannot be read: .+ \(line 2\)$`).MatchString(e.Message) {
			t.Errorf("message %q, want \"bob.lock cannot be read: <reason> (line 2)\"", e.Message)
		}
		if want := "resolve the merge conflict, or run bob lock --rebuild to resolve everything again from bob.go"; e.Hint != want {
			t.Errorf("hint %q, want %q", e.Hint, want)
		}
	}
	if r := bobRun(t, dir, "lock", "--rebuild"); !exitIs(t, r, 0) {
		return
	}
	var again lock.NixpkgsPin
	if err := json.Unmarshal(sections(t, []byte(readFile(t, path)))["nixpkgs"], &again); err != nil || again.Rev != pin.Rev {
		t.Errorf("bob lock --rebuild changed the nixpkgs rev: %q → %q (%v)", pin.Rev, again.Rev, err)
	}
	checkCanonical(t, []byte(readFile(t, path)))
}

// R-FW-062: after a requirement is added in bob.go, bob build exits 3 with BOB-E-1012 and no
// network request (covered with R-FW-005 above); here every ecosystem's section is checked
// on its own fixture: adding a declaration makes exactly that ecosystem stale.
func TestRFW062StaleSectionPerEcosystem(t *testing.T) {
	parity.Covers(t, "R-FW-062")
	cases := []struct {
		fixture, file, old, new, eco string
	}{
		{"acme", "bob.go", `golang.Require("github.com/google/uuid", "v1.6.0")`, `golang.Require("github.com/google/uuid", "v1.5.0")`, "go"},
		{"acme", "crates/rcore/bob.go", `rust.Crate("hex", "0.4")`, `rust.Crate("hex", "0.4"), rust.Crate("base64", "0.22")`, "rust"},
		{"acme", "web/bob.go", `npm.Dep("svelte", "^5")`, `npm.Dep("svelte", "^5"), npm.Dep("zod", "^3")`, "npm"},
	}
	for _, c := range cases {
		t.Run(c.eco, func(t *testing.T) {
			dir := harness.Fixture(t, c.fixture)
			bobHome := t.TempDir()
			if r := bobRunEnv(t, dir, []string{"BOB_HOME=" + bobHome}, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
				return
			}
			replaceIn(t, filepath.Join(dir, c.file), c.old, c.new)
			r := bobNoNetworkHome(t, dir, bobHome, "build", "//...", "--json")
			if !exitIs(t, r, 3) {
				return
			}
			e, ok := errorWithCode(t, r, "BOB-E-1012")
			if ok && !namesEcosystem(e.Message, c.eco) {
				t.Errorf("the stale-lock error does not name %s: %q", c.eco, e.Message)
			}
		})
	}
}

// sectionSeeds are valid sections of every ecosystem (SPEC §03.7).
var sectionSeeds = map[string]string{
	"go":   `{"inputs":"` + zeros64 + `","modules":[{"modH1":"h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=","packages":false,"path":"github.com/google/go-cmp","version":"v0.7.0"}],"proxy":"https://proxy.golang.org","workspaces":{"example.com/api":{"go":"1.26.0","modules":["github.com/google/go-cmp@v0.7.0"]}}}`,
	"rust": `{"crates":[{"checksum":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=","meta":{"edition":"2021"},"name":"itoa","narHash":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=","version":"1.0.15"}],"index":"sparse+https://index.crates.io/","inputs":"` + zeros64 + `"}`,
	"npm":  `{"importers":{"web":{"deps":{"react":"react@19.3.1"}}},"inputs":"` + zeros64 + `","packages":{"react@19.3.1":{"integrity":"sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg==","name":"react","version":"19.3.1"}},"registry":"https://registry.npmjs.org/"}`,
	"opam": `{"inputs":"` + zeros64 + `","packages":{"fmt":{"hash":"sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg==","opam":"02bb57a07d169eef9bde16e0a50a00423a4f756d589f7d1fed0d4d3aa367d11c","url":"https://erratique.ch/software/fmt/releases/fmt-0.11.0.tbz","version":"0.11.0"}},"repository":{"commit":"af5ddb0addf6e75660ee9d7485b27425459d1d66","narHash":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=","url":"github:ocaml/opam-repository"}}`,
	"cc":   `{"externals":{"//third_party/zstd:zstd":{"narHash":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=","strip":1,"url":"https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz"}},"inputs":"` + zeros64 + `"}`,
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"

// invalidSections are mutations of the seeds the decoders must reject (hashes, names).
var invalidSections = []struct{ key, old, new, what string }{
	{"go", `h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=`, `sha256-pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=`, "modH1 without h1: prefix"},
	{"go", `h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=`, `h1:!!!`, "modH1 not base64"},
	{"go", `"version":"v0.7.0"`, `"version":"0.7"`, "version not semver"},
	{"go", `github.com/google/go-cmp","version`, `github.com/google/go cmp","version`, "path with a space"},
	{"go", `github.com/google/go-cmp","version`, `../etc/passwd","version`, "path escaping"},
	{"go", `"proxy":"https://proxy.golang.org"`, `"proxy":"file:///etc"`, "proxy not http(s)"},
	{"rust", `"checksum":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0="`, `"checksum":"md5-abc"`, "checksum not SRI"},
	{"rust", `"narHash":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0="`, `"narHash":"sha256-short"`, "narHash wrong length"},
	{"rust", `"name":"itoa"`, `"name":"../itoa"`, "crate name with a slash"},
	{"rust", `"version":"1.0.15"`, `"version":"one"`, "crate version not semver"},
	{"npm", `sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg==`, `md5-xyz`, "integrity not SRI"},
	{"npm", `"name":"react"`, `"name":"../react"`, "package name escaping"},
	{"npm", `"registry":"https://registry.npmjs.org/"`, `"registry":"ftp://x/"`, "registry not http(s)"},
	{"opam", `"commit":"af5ddb0addf6e75660ee9d7485b27425459d1d66"`, `"commit":"af5ddb0"`, "commit not 40 hex"},
	{"opam", `"opam":"02bb57a07d169eef9bde16e0a50a00423a4f756d589f7d1fed0d4d3aa367d11c"`, `"opam":"zz"`, "opam digest not sha256 hex"},
	{"opam", `"hash":"sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg=="`, `"hash":"nope"`, "hash not SRI"},
	{"cc", `"narHash":"sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0="`, `"narHash":"x"`, "narHash not SRI"},
	{"cc", `"strip":1`, `"strip":-1`, "negative strip"},
}

// R-FW-065 (a): decoding and re-encoding each section is value-identical and the encoding is
// canonical (keys sorted); (b) invalid hashes and names are rejected.
func TestRFW065SectionRoundTrip(t *testing.T) {
	parity.Covers(t, "R-FW-065")
	keys := make([]string, 0, len(sectionSeeds))
	for k := range sectionSeeds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			v, err := lock.DecodeSection(key, json.RawMessage(sectionSeeds[key]))
			if isNotImplemented(err) {
				harness.RedFeature(t, "lock.DecodeSection")
				return
			}
			if err != nil {
				t.Fatalf("DecodeSection(%s) of a valid section: %v", key, err)
			}
			out, err := lock.EncodeSection(key, v)
			if err != nil {
				t.Fatalf("EncodeSection(%s): %v", key, err)
			}
			if !bytes.Equal(compactJSON(t, out), compactJSON(t, json.RawMessage(sectionSeeds[key]))) {
				t.Errorf("re-encoding %s is not byte-identical (compact form):\n got  %s\n want %s", key, compactJSON(t, out), compactJSON(t, json.RawMessage(sectionSeeds[key])))
			}
			again, err := lock.DecodeSection(key, out)
			if err != nil {
				t.Fatalf("decoding the encoding: %v", err)
			}
			out2, err := lock.EncodeSection(key, again)
			if err != nil || !bytes.Equal(out, out2) {
				t.Errorf("encoding is not a fixed point: %v", err)
			}
		})
	}
	for _, c := range invalidSections {
		t.Run("invalid/"+c.key+"/"+c.what, func(t *testing.T) {
			seed := sectionSeeds[c.key]
			if !strings.Contains(seed, c.old) {
				t.Fatalf("harness: seed of %s lacks %q", c.key, c.old)
			}
			mutated := strings.Replace(seed, c.old, c.new, 1)
			_, err := lock.DecodeSection(c.key, json.RawMessage(mutated))
			if isNotImplemented(err) {
				harness.RedFeature(t, "lock.DecodeSection")
				return
			}
			if err == nil {
				t.Errorf("DecodeSection(%s) accepted a section with %s", c.key, c.what)
			}
		})
	}
	t.Run("unknown section", func(t *testing.T) {
		_, err := lock.DecodeSection("pip", json.RawMessage(`{"inputs":"x"}`))
		if isNotImplemented(err) {
			harness.RedFeature(t, "lock.DecodeSection")
			return
		}
		if err == nil {
			t.Errorf("DecodeSection accepted the unknown section key \"pip\"")
		}
	})
}

func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("not JSON: %v: %s", err, raw)
	}
	return b.Bytes()
}

// FuzzLockSection: the section decoders never panic on arbitrary input and accept only what
// re-encodes (R-FW-065). The slow suite runs it with -fuzz and 10^5 iterations.
func FuzzLockSection(f *testing.F) {
	parity.Covers(f, "R-FW-065")
	for _, key := range []string{"go", "rust", "npm", "opam", "cc"} {
		f.Add(key, []byte(sectionSeeds[key]))
	}
	f.Add("go", []byte(`{"modules":[{"path":"`+strings.Repeat("a", 70000)+`"}]}`))
	f.Add("npm", []byte(`{"packages":null,"importers":[]}`))
	f.Fuzz(func(t *testing.T, key string, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeSection(%q) panicked on %.200q: %v", key, data, r)
			}
		}()
		v, err := lock.DecodeSection(key, json.RawMessage(data))
		if isNotImplemented(err) {
			harness.RedFeature(t, "lock.DecodeSection")
			return
		}
		if err != nil {
			return
		}
		out, err := lock.EncodeSection(key, v)
		if err != nil {
			t.Fatalf("a decoded %s section does not encode: %v", key, err)
		}
		if _, err := lock.DecodeSection(key, out); err != nil {
			t.Fatalf("the encoding of a decoded %s section is rejected: %v", key, err)
		}
	})
}
