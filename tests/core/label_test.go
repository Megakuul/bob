package core

// SPEC §1.3 labels and §1.9 configured labels: R-FW-015, R-FW-016, R-FW-050.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// labelCase is one row of the R-FW-015 table. For valid rows, want is the parsed label;
// every valid row is canonical, so String() must give s back.
//
// Ruling G9 (plan/gate-h-rulings.md): bob.Parse accepts the union of label, pattern and
// configured forms and returns a typed value; ParseLabel accepts labels only. valid is the
// verdict of Parse; parseOnly marks the rows that are valid for Parse but not labels (their want
// is unused, the type is checked instead). Bounds: whole string <= 1024 bytes, one seg <= 255.
type labelCase struct {
	s         string
	valid     bool
	want      bob.Label
	parseOnly bool
}

func labelTable() []labelCase {
	seg := func(n int) string { return strings.Repeat("d", n) }
	dir1000 := seg(250) + "/" + seg(250) + "/" + seg(250) + "/" + seg(247) // 1000 bytes, segs <= 255
	name129 := strings.Repeat("a", 129)
	name128 := strings.Repeat("a", 128)
	return []labelCase{
		{"//:x", true, bob.Label{Name: "x"}, false},
		{"//api:api", true, bob.Label{Dir: "api", Name: "api"}, false},
		{"//libs/foo:foo", true, bob.Label{Dir: "libs/foo", Name: "foo"}, false},
		{"//a/b:c", true, bob.Label{Dir: "a/b", Name: "c"}, false},
		{"//a/b/c/d:e", true, bob.Label{Dir: "a/b/c/d", Name: "e"}, false},
		{"//third_party/zstd:zstd", true, bob.Label{Dir: "third_party/zstd", Name: "zstd"}, false},
		{"//a-b/c.d/E_f:g", true, bob.Label{Dir: "a-b/c.d/E_f", Name: "g"}, false},
		{"//a/.hidden:x", true, bob.Label{Dir: "a/.hidden", Name: "x"}, false},
		{"//a:0", true, bob.Label{Dir: "a", Name: "0"}, false},
		{"//a:1x", true, bob.Label{Dir: "a", Name: "1x"}, false},
		{"//a:x_y-z.w", true, bob.Label{Dir: "a", Name: "x_y-z.w"}, false},
		{"//a:" + name128, true, bob.Label{Dir: "a", Name: name128}, false},
		{"example.com/o//libs/bar:bar", true, bob.Label{Module: "example.com/o", Dir: "libs/bar", Name: "bar"}, false},
		{"example.com/o//:root", true, bob.Label{Module: "example.com/o", Name: "root"}, false},
		{"github.com/megakuul/bob//cmd/bob:bob", true, bob.Label{Module: "github.com/megakuul/bob", Dir: "cmd/bob", Name: "bob"}, false},
		{"nixpkgs:protobuf", true, bob.Label{Nix: "protobuf"}, false},
		{"nixpkgs:python3Packages.numpy", true, bob.Label{Nix: "python3Packages.numpy"}, false},
		{"nixpkgs:gnome-shell-extension_x", true, bob.Label{Nix: "gnome-shell-extension_x"}, false},
		{"nixpkgs:perl5.40.0", true, bob.Label{Nix: "perl5.40.0"}, false},
		{"nixpkgs:it's", true, bob.Label{Nix: "it's"}, false},

		{"", false, bob.Label{}, false},
		{"//", false, bob.Label{}, false},
		{":x", false, bob.Label{}, false},
		{"//a", true, bob.Label{}, true}, // a pattern (= //a:a): valid for Parse, not a label
		{"//a/b", true, bob.Label{}, true},
		{"//..", false, bob.Label{}, false},
		{"//a:", false, bob.Label{}, false},
		{"//api:API", false, bob.Label{}, false},
		{"//api:-x", false, bob.Label{}, false},
		{"//api:_x", false, bob.Label{}, false},
		{"//api:.x", false, bob.Label{}, false},
		{"//a:" + name129, false, bob.Label{}, false},
		{"//a/../b:x", false, bob.Label{}, false},
		{"//a/./b:x", false, bob.Label{}, false},
		{"//./a:x", false, bob.Label{}, false},
		{"//a//b:x", false, bob.Label{}, false},
		{"///a:x", false, bob.Label{}, false},
		{"//a/:x", false, bob.Label{}, false},
		{"//a b:x", false, bob.Label{}, false},
		{"//a:b c", false, bob.Label{}, false},
		{"//a:b+c", false, bob.Label{}, false},
		{"//a:b:c", false, bob.Label{}, false},
		{"//a:b/c", false, bob.Label{}, false},
		{"//a/b$:x", false, bob.Label{}, false},
		{"//api:api@linux-amd64+debug+asan", true, bob.Label{}, true}, // configured: valid for Parse only
		{"//a:b\n", false, bob.Label{}, false},
		{"//ä:x", false, bob.Label{}, false},
		{"nixpkgs:", false, bob.Label{}, false},
		{"nixpkgs:a b", false, bob.Label{}, false},
		{"nixpkgs:a/b", false, bob.Label{}, false},
		{"nixpkgs:a:b", false, bob.Label{}, false},
		{"nixpkgs://a:b", false, bob.Label{}, false},
		{"example.com/o//a:Bad", false, bob.Label{}, false},
		{"example.com/o///a:b", false, bob.Label{}, false},
		{"//" + dir1000 + ":x", true, bob.Label{Dir: dir1000, Name: "x"}, false},              // 1000-byte dir is valid
		{"//" + seg(255) + ":x", true, bob.Label{Dir: seg(255), Name: "x"}, false},            // seg at the cap
		{"//" + seg(256) + ":x", false, bob.Label{}, false},                                   // seg above the cap
		{"//" + strings.Repeat(seg(250)+"/", 4) + seg(250) + ":x", false, bob.Label{}, false}, // segs fine, whole string > 1024
		{"//" + strings.Repeat("d", 5000) + ":x", false, bob.Label{}, false},                  // 5000-byte dir is invalid
	}
}

// R-FW-015 (ruling G9): ≥ 30 valid and invalid strings through bob.Parse (union of label, pattern,
// configured); ParseLabel accepts labels only; Parse(s).String()==s for canonical inputs.
func TestRFW015LabelGrammar(t *testing.T) {
	parity.Covers(t, "R-FW-015")
	table := labelTable()
	if len(table) < 30 {
		t.Fatalf("table has %d rows, SPEC wants ≥ 30", len(table))
	}
	for i, c := range table {
		s := c.s
		if len(s) > 60 {
			s = fmt.Sprintf("%.40s…(%d bytes)", s, len(c.s))
		}
		t.Run(fmt.Sprintf("%02d/%s", i, strings.ReplaceAll(s, "/", "|")), func(t *testing.T) {
			ref, err := bob.Parse(c.s)
			if isNotImplemented(err) {
				harness.RedFeature(t, "bob.Parse")
				return
			}
			_, lerr := bob.ParseLabel(c.s)
			if isNotImplemented(lerr) {
				harness.RedFeature(t, "bob.ParseLabel")
				return
			}
			if !c.valid {
				if err == nil {
					t.Errorf("Parse(%q) = %#v, want an error", c.s, ref)
				}
				if lerr == nil {
					t.Errorf("ParseLabel(%q) accepted an invalid string", c.s)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.s, err)
			}
			if ref == nil || ref.String() != c.s {
				t.Errorf("round trip: Parse(%q).String() = %v", c.s, ref)
			}
			if c.parseOnly {
				if _, isLabel := ref.(bob.Label); isLabel {
					t.Errorf("Parse(%q) returned a Label, want a pattern or configured value", c.s)
				}
				if lerr == nil {
					t.Errorf("ParseLabel(%q) accepted a non-label form", c.s)
				}
				return
			}
			got, isLabel := ref.(bob.Label)
			if !isLabel {
				t.Fatalf("Parse(%q) = %T, want a bob.Label", c.s, ref)
			}
			if got.Module != c.want.Module || got.Dir != c.want.Dir || got.Name != c.want.Name || got.Nix != c.want.Nix {
				t.Errorf("Parse(%q) = %+v, want %+v", c.s, got, c.want)
			}
			if lerr != nil {
				t.Fatalf("ParseLabel(%q): %v", c.s, lerr)
			}
			if lgot, _ := bob.ParseLabel(c.s); lgot != got {
				t.Errorf("ParseLabel(%q) = %+v differs from Parse's %+v", c.s, lgot, got)
			}
		})
	}
}

// R-FW-015: a duplicate label is reported as "<pos>: package <label> is already defined at <pos2>".
func TestRFW015DuplicateLabel(t *testing.T) {
	parity.Covers(t, "R-FW-015")
	dir := repo(t, "duplabel")
	r := bobRun(t, dir, "graph", "--json", "//...")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	var msgs []string
	for _, e := range errorsOf(t, events(t, r)) {
		msgs = append(msgs, e.Message)
	}
	re := regexp.MustCompile(`^dup/bob\.go:\d+: package //dup:x is already defined at dup/bob\.go:\d+$`)
	for _, m := range msgs {
		if re.MatchString(m) {
			return
		}
	}
	t.Errorf("no error says \"<pos>: package //dup:x is already defined at <pos2>\"; got %q", msgs)
}

// R-FW-016: every IR node's definedAt is the line of the outermost constructor call: the line
// of a bob.go that names the node's label name as a string literal (cc.SharedLibrary(p,
// "foo", …)), module-relative.
func TestRFW016DefinedAtIsConstructorLine(t *testing.T) {
	parity.Covers(t, "R-FW-016")
	dir := harness.Fixture(t, "acme")
	res, ok := evaluate(t, dir)
	if !ok {
		return
	}
	var head struct {
		BobVersion string `json:"bobVersion"`
	}
	if err := json.Unmarshal(res.IR, &head); err != nil {
		t.Fatalf("IR is not JSON: %v", err)
	}
	g, err := ir.Parse(res.IR, head.BobVersion)
	if isNotImplemented(err) {
		harness.RedFeature(t, "ir.Parse")
		return
	}
	if err != nil {
		t.Fatalf("ir.Parse: %v", err)
	}
	checked := 0
	for _, n := range g.Nodes {
		if strings.HasPrefix(n.Label, "nixpkgs:") || n.DefinedAt.File == "" {
			continue
		}
		file := filepath.Join(dir, filepath.FromSlash(n.DefinedAt.File))
		if filepath.Base(file) != "bob.go" {
			t.Errorf("node %s: definedAt file %q is not a bob.go", n.ID, n.DefinedAt.File)
			continue
		}
		lines := strings.Split(readFile(t, file), "\n")
		if n.DefinedAt.Line < 1 || n.DefinedAt.Line > len(lines) {
			t.Errorf("node %s: definedAt %s:%d is outside the file", n.ID, n.DefinedAt.File, n.DefinedAt.Line)
			continue
		}
		// The line holds the constructor call (it passes p); the declaration line above it
		// ("var Lib = bob.Define(…)") and the closing lines do not.
		line := lines[n.DefinedAt.Line-1]
		if !strings.Contains(line, "(p") || strings.Contains(line, "bob.Define(") {
			t.Errorf("node %s: definedAt %s:%d is not the constructor call: %q", n.ID, n.DefinedAt.File, n.DefinedAt.Line, line)
		}
		checked++
	}
	if checked < 4 {
		t.Errorf("only %d nodes of acme were checked", checked)
	}
	// The line heuristic above accepts any line that passes p; pin the exact constructor line
	// of the packages whose bob.go (tests/fixtures/repos/acme) names their label in a comment.
	exact := map[string]struct{ file, call string }{
		"//libs/foo:foo":       {"libs/foo/bob.go", "cc.SharedLibrary(p"},
		"//crates/rcore:rcore": {"crates/rcore/bob.go", "rust.StaticLib(p"},
		"//web:web":            {"web/bob.go", "npm.Vite(p"},
	}
	for label, want := range exact {
		line := 0
		for i, l := range strings.Split(readFile(t, filepath.Join(dir, filepath.FromSlash(want.file))), "\n") {
			if strings.Contains(l, want.call) {
				line = i + 1
			}
		}
		found := false
		for _, n := range g.Nodes {
			if n.Label == label {
				found = true
				if n.DefinedAt.File != want.file || n.DefinedAt.Line != line {
					t.Errorf("node %s: definedAt %s:%d, want %s:%d (the %s call)", label, n.DefinedAt.File, n.DefinedAt.Line, want.file, line, want.call)
				}
			}
		}
		if !found {
			t.Errorf("acme's IR has no node %s", label)
		}
	}
}

// R-FW-016: a failing C++ build of //libs/foo:foo prints "(defined at libs/foo/bob.go:13)".
// Ruling G8: definedAt is the line of the call to the ecosystem constructor (the first stack frame
// outside github.com/megakuul/bob/), line 13 of libs/foo/bob.go; line 12 is the `var Lib =
// bob.Define(` line that R-FW-001 uses. The test reads the constructor line from the file.
func TestRFW016FailingBuildShowsDefinedAt(t *testing.T) {
	parity.Covers(t, "R-FW-016")
	dir := harness.Fixture(t, "acme")
	writeFile(t, filepath.Join(dir, "libs", "foo", "src", "broken.cpp"), "int broken( { return; }\n")
	r := bobRun(t, dir, "build", "//libs/foo:foo")
	if harness.Red(t, r) || !exitIs(t, r, 1) {
		return
	}
	line := 0
	for i, l := range strings.Split(readFile(t, filepath.Join(dir, "libs", "foo", "bob.go")), "\n") {
		if strings.Contains(l, "cc.SharedLibrary(p") {
			line = i + 1
		}
	}
	want := fmt.Sprintf("(defined at libs/foo/bob.go:%d)", line)
	if line == 0 || !strings.Contains(r.Stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, r.Stderr)
	}
}

// R-FW-050: ParseConfigured table. Host is linux-arm64.
func TestRFW050ParseConfigured(t *testing.T) {
	parity.Covers(t, "R-FW-050")
	const host = bob.LinuxARM64
	named := []string{"fuzz", "ci"}
	ok := []struct{ in, canonical string }{
		{"//api:api@+asan+debug", "//api:api@linux-arm64+debug+asan"},
		{"//api:api@linux-amd64+debug+asan", "//api:api@linux-amd64+debug+asan"},
		{"//api:api@linux-amd64+asan+debug", "//api:api@linux-amd64+debug+asan"},
		{"//api:api@", "//api:api@linux-arm64"},
		{"//api:api@linux-arm64", "//api:api@linux-arm64"},
		{"//api:api@+release", "//api:api@linux-arm64"},
		{"//api:api@+ubsan+asan", "//api:api@linux-arm64+asan+ubsan"},
		{"//api:api@+race+ubsan+asan+debug", "//api:api@linux-arm64+debug+asan+ubsan+race"},
		{"//api:api@+fuzz+ci", "//api:api@linux-arm64+ci+fuzz"},
		{"//api:api@linux-amd64+fuzz+race+ci", "//api:api@linux-amd64+race+ci+fuzz"},
		{"example.com/o//libs/bar:bar@linux-amd64+debug", "example.com/o//libs/bar:bar@linux-amd64+debug"},
	}
	bad := []struct{ in, wantErr string }{
		{"//api:api@linux-x86", `unknown platform "linux-x86" (known: linux-amd64, linux-arm64)`},
		{"//api:api@darwin-arm64+debug", `unknown platform "darwin-arm64" (known: linux-amd64, linux-arm64)`},
		{"//api:api@+nope", `unknown variant "nope" (known: `},
		{"//api:api@linux-amd64+asan+nope", `unknown variant "nope" (known: `},
		{"//api:API@+asan", ""},
		{"//api:api+asan", ""},
		{"//api:api@+", ""},
		{"//api:api@++asan", ""},
	}
	for _, c := range ok {
		t.Run(c.in, func(t *testing.T) {
			got, err := bob.ParseConfigured(c.in, host, named...)
			if isNotImplemented(err) {
				harness.RedFeature(t, "bob.ParseConfigured")
				return
			}
			if err != nil {
				t.Fatalf("ParseConfigured(%q): %v", c.in, err)
			}
			if got.String() != c.canonical {
				t.Errorf("ParseConfigured(%q).String() = %q, want %q", c.in, got.String(), c.canonical)
			}
			again, err := bob.ParseConfigured(c.canonical, host, named...)
			if err != nil || again.String() != c.canonical {
				t.Errorf("canonical form %q does not round trip: %q, %v", c.canonical, again.String(), err)
			}
		})
	}
	for _, c := range bad {
		t.Run(c.in, func(t *testing.T) {
			_, err := bob.ParseConfigured(c.in, host, named...)
			if isNotImplemented(err) {
				harness.RedFeature(t, "bob.ParseConfigured")
				return
			}
			if err == nil {
				t.Fatalf("ParseConfigured(%q) succeeded, want an error", c.in)
			}
			if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("ParseConfigured(%q) error %q, want it to contain %q", c.in, err, c.wantErr)
			}
		})
	}
	// "known: …" lists the repository's named variants next to the built-in tokens.
	_, err := bob.ParseConfigured("//api:api@+nope", host, named...)
	for _, tok := range []string{"release", "debug", "asan", "ubsan", "race", "ci", "fuzz"} {
		if err != nil && !isNotImplemented(err) && !strings.Contains(err.Error(), tok) {
			t.Errorf("unknown-variant error does not list %q: %v", tok, err)
		}
	}
}
