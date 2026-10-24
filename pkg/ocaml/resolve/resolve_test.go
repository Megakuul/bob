package resolve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// repoOf builds a repository from "name.version" → opam file text.
func repoOf(files map[string]string) *Repository {
	m := fstest.MapFS{}
	for nv, body := range files {
		name, _, _ := strings.Cut(nv, ".")
		m["packages/"+name+"/"+nv+"/opam"] = &fstest.MapFile{Data: []byte(body)}
	}
	return NewRepository(m)
}

const src = `url { src: "https://example.org/x.tbz" checksum: ["sha256=` +
	"0000000000000000000000000000000000000000000000000000000000000000" + `"] }
`

// pkgf is an opam file with the given depends body (and a source).
func pkgf(depends string, extra ...string) string {
	return "opam-version: \"2.0\"\ndepends: [" + depends + "]\n" + strings.Join(extra, "\n") + "\n" + src
}

func tc() map[string]string { return Toolchain("5.4.1", "3.21.1", "1.9.8") }

func solve(t *testing.T, r *Repository, reqs ...Request) map[string]string {
	t.Helper()
	sol, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, reqs)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if !sol.Optimal {
		t.Errorf("not proven optimal after %d steps", sol.Steps)
	}
	validate(t, r, "arm64", tc(), reqs, sol)
	got := map[string]string{}
	for _, p := range sol.Packages {
		got[p.Name] = p.Version
	}
	return got
}

func req(names ...string) []Request {
	var out []Request
	for _, n := range names {
		name, c, _ := strings.Cut(n, " ")
		out = append(out, Request{Name: name, Constraint: c})
	}
	return out
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.1", -1},
		{"1.10", "1.9", 1},
		{"1.0~beta", "1.0", -1},
		{"1.0", "1.0+x", -1},
		{"1.0a", "1.0+", -1}, // letters before other characters
		{"v0.17.0", "v0.16.9", 1},
		{"01", "1", 0},
		{"3.21.1", "3.22", -1},
		{"1.0.0~alpha8", "1.0.0~alpha10", -1},
		{"5.6.1-504", "5.6.1-503", 1},
		{"", "0", 0}, // empty digit runs are 0
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func TestParse(t *testing.T) {
	f, err := Parse([]byte(`opam-version: "2.0"
# comment
(* block
   comment *)
synopsis: """triple "quoted" text"""
descr: "a\tb\
        c"
depends: [
  "ocaml" {>= "4.08" & < "5.5"}
  ("a" | "b" {build}) & "c"
  "d" {= version}
]
available: arch != "x86_32" & !(os = "win32")
build: [["dune" "build" "-p" name "-j" jobs] {with-test}]
setenv: [FOO += "x"]
url { src: "https://e/x.tgz" checksum: "sha512=ab" }
extra-source "p.patch" { src: "https://e/p" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := StrOf(f.Fields["synopsis"]); got != `triple "quoted" text` {
		t.Errorf("triple string = %q", got)
	}
	if got := StrOf(f.Fields["descr"]); got != "a\tbc" {
		t.Errorf("escaped string = %q", got)
	}
	if n := len(Items(f.Fields["depends"])); n != 3 {
		t.Errorf("depends has %d items, want 3", n)
	}
	if f.Sections["url"] == nil || f.Sections["extra-source p.patch"] == nil {
		t.Errorf("sections = %v", f.Sections)
	}
	if v := Items(f.Fields["setenv"])[0]; v.Kind != EnvOp || v.S != "+=" {
		t.Errorf("setenv = %+v", v)
	}
	env := func(n string) (string, bool) {
		m := map[string]string{"arch": "arm64", "os": "linux"}
		v, ok := m[n]
		return v, ok
	}
	if b, ok := EvalFilter(f.Fields["available"], env); !ok || !b {
		t.Errorf("available = %v %v, want true", b, ok)
	}
}

func TestParseRejectsHostileInput(t *testing.T) {
	for name, in := range map[string]string{
		"deep nesting":        "x: " + strings.Repeat("[", 1000) + strings.Repeat("]", 1000),
		"deep negation":       "x: " + strings.Repeat("!", 1000) + "a",
		"unterminated string": `x: "abc`,
		"unterminated list":   `x: [ "a"`,
		"unterminated option": `x: "a" { b`,
		"unterminated cmt":    `(* x`,
		"bad char":            "x: \x00",
		"stray brace":         "}",
		"too large":           "x: \"" + strings.Repeat("a", maxFileBytes) + "\"",
	} {
		if _, err := Parse([]byte(in)); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: err = %v, want ErrSyntax", name, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(pkgf(`"a" {>= "1" & (build | !post)} ("b" | "c")`)))
	f.Add([]byte(`x: [ ?a !b c:d "%{e?f:g}%" 12 true ] y { z: 1 }`))
	f.Fuzz(func(t *testing.T, b []byte) {
		file, err := Parse(b)
		if err != nil {
			return
		}
		env := func(string) (string, bool) { return "", false }
		for _, v := range file.Fields {
			_, _ = Deps(v, env)
			_, _ = EvalFilter(v, env)
		}
	})
}

func TestDepsFilters(t *testing.T) {
	f, err := Parse([]byte(`depends: [
  "a" {build}
  "b" {with-test}
  "c" {>= "1.0" & with-doc}
  "d" {= version}
  "e" {post}
  "f" {with-test | >= "2"}
  "g" {!with-test & < "3"}
  ("h" | "i") & "j"
  "k" {undefined-var}
]`))
	if err != nil {
		t.Fatal(err)
	}
	env := func(n string) (string, bool) {
		v, ok := map[string]string{"version": "1.2", "build": "true", "post": "true",
			"with-test": "false", "with-doc": "false"}[n]
		return v, ok
	}
	form, err := Deps(f.Fields["depends"], env)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range form {
		got = append(got, clauseString(c))
	}
	want := []string{`a`, `d = "1.2"`, `e`, `f >= "2"`, `g < "3"`, `h & j | i & j`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Deps = %q, want %q", got, want)
	}
}

func TestDepsBoundsAlternatives(t *testing.T) {
	var parts []string
	for i := 0; i < 12; i++ {
		parts = append(parts, fmt.Sprintf(`("a%d" | "b%d")`, i, i))
	}
	f, err := Parse([]byte("depends: [" + strings.Join(parts, " & ") + "]"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Deps(f.Fields["depends"], func(string) (string, bool) { return "", false }); !errors.Is(err, ErrSyntax) {
		t.Errorf("2^12 alternatives: err = %v, want ErrSyntax", err)
	}
}

func TestParseConstraint(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		">= 5.7":                 `>= "5.7"`,
		"= 1.26.0":               `= "1.26.0"`,
		`>= "1.0" & < "2.0"`:     `>= "1.0" & < "2.0"`,
		">= v0.16 & < v0.17":     `>= "v0.16" & < "v0.17"`,
		"< 2.0~ | >= 3":          `(< "2.0~" | >= "3")`,
		"!= 1.0.0~alpha8":        `!= "1.0.0~alpha8"`,
		"  >=   1.2.3+foo-bar  ": `>= "1.2.3+foo-bar"`,
	} {
		c, err := ParseConstraint(in)
		if err != nil {
			t.Errorf("ParseConstraint(%q): %v", in, err)
			continue
		}
		if got := c.String(); got != want {
			t.Errorf("ParseConstraint(%q) = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"foo", ">=", "with-test", strings.Repeat(">= 1 & ", 100), `>= "1`} {
		if _, err := ParseConstraint(bad); err == nil {
			t.Errorf("ParseConstraint(%q): no error", bad)
		}
	}
}

func TestSolveNewestAndConstraints(t *testing.T) {
	r := repoOf(map[string]string{
		"a.1.0": pkgf(`"b"`),
		"a.2.0": pkgf(`"b" {>= "2.0"}`),
		"b.1.0": pkgf(``),
		"b.2.0": pkgf(``),
		"b.3.0": pkgf(`"ocaml" {< "5.0"}`), // never installable with the toolchain
		"c.1.0": pkgf(`"dune" {>= "3.0"} "ocaml"`),
	})
	if got, want := solve(t, r, req("a", "c")...), map[string]string{"a": "2.0", "b": "2.0", "c": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := solve(t, r, req("a < 2.0")...), map[string]string{"a": "1.0", "b": "2.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a < 2.0: got %v, want %v", got, want)
	}
}

// opam minimises the total version lag, not the lag of each package in turn: an older root
// dependency is better when the newest one drags in packages that are only available in old
// versions with the fixed toolchain.
func TestSolveMinimisesTotalLag(t *testing.T) {
	r := repoOf(map[string]string{
		"app.1.0":  pkgf(`"lib"`),
		"lib.1.0":  pkgf(``),
		"lib.2.0":  pkgf(`"site"`),
		"site.1.0": pkgf(`"dune" {>= "3.21"}`),
		"site.2.0": pkgf(`"dune" {>= "3.22"}`),
		"site.3.0": pkgf(`"dune" {>= "3.23"}`),
	})
	// lib 2.0 (lag 0) needs site 1.0 (lag 2): total 2; lib 1.0 (lag 1) alone: total 1.
	if got, want := solve(t, r, req("app")...), map[string]string{"app": "1.0", "lib": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// The lag of requested packages comes first.
	if got, want := solve(t, r, req("lib")...), map[string]string{"lib": "2.0", "site": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("request lib: got %v, want %v", got, want)
	}
}

func TestSolveAvoidVersionAndAvailability(t *testing.T) {
	r := repoOf(map[string]string{
		"a.1.0":     pkgf(``),
		"a.2.0~rc1": pkgf(``, "flags: avoid-version"),
		"a.3.0":     pkgf(``, `available: arch = "x86_64"`),
		"a.4.0":     pkgf(``, `available: os = "macos"`),
	})
	if got := solve(t, r, req("a")...); got["a"] != "1.0" {
		t.Errorf("a = %s, want 1.0 (2.0~rc1 is avoid-version, 3.0 x86_64 only, 4.0 macos only)", got["a"])
	}
	sol, err := Solve(context.Background(), r, Options{Arch: "x86_64", Provided: tc()}, req("a"))
	if err != nil || sol.Packages[0].Version != "3.0" {
		t.Errorf("x86_64: %v %v, want a 3.0", sol, err)
	}
}

func TestSolveConflicts(t *testing.T) {
	r := repoOf(map[string]string{
		"app.1.0": pkgf(`"x" "y"`),
		"x.1.0":   pkgf(``),
		"x.2.0":   pkgf(``, `conflicts: ["y" {>= "2.0"}]`),
		"y.1.0":   pkgf(``),
		"y.2.0":   pkgf(``),
		"z.1.0":   pkgf(``, `conflict-class: "ocaml-core-compiler"`),
		"w.1.0":   pkgf(``, `conflicts: "dune"`),
		"v.1.0":   pkgf(``, `conflict-class: "k"`),
		"u.1.0":   pkgf(``, `conflict-class: "k"`),
		"both.1":  pkgf(`"v" "u"`),
	})
	// x 2.0 + y 1.0 and x 1.0 + y 2.0 both lag 1; the requested app has no alternatives.
	got := solve(t, r, req("app")...)
	if got["x"] == "2.0" && got["y"] == "2.0" {
		t.Errorf("x 2.0 conflicts with y 2.0: %v", got)
	}
	r2 := repoOf(map[string]string{
		"z.1.0":                     pkgf(``, `conflict-class: "ocaml-core-compiler"`),
		"w.1.0":                     pkgf(``, `conflicts: "dune"`),
		"v.1.0":                     pkgf(``, `conflict-class: "k"`),
		"u.1.0":                     pkgf(``, `conflict-class: "k"`),
		"both.1":                    pkgf(`"v" "u"`),
		"ocaml-base-compiler.5.4.1": "opam-version: \"2.0\"\nconflict-class: \"ocaml-core-compiler\"\n",
	})
	for _, root := range []string{"z", "w", "both"} {
		_, err := Solve(context.Background(), r2, Options{Arch: "arm64", Provided: tc()}, req(root))
		if !errors.Is(err, ErrUnsatisfiable) {
			t.Errorf("%s: err = %v, want ErrUnsatisfiable", root, err)
		}
	}
}

func TestSolveDisjunction(t *testing.T) {
	r := repoOf(map[string]string{
		"app.1.0": pkgf(`("a" {>= "2"} | "b") "c"`),
		"a.1.0":   pkgf(``),
		"b.1.0":   pkgf(``),
		"c.1.0":   pkgf(`"a"`),
	})
	// a >= 2 does not exist: app needs b, and c pulls in a 1.0.
	if got, want := solve(t, r, req("app")...), map[string]string{"app": "1.0", "a": "1.0", "b": "1.0", "c": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	r = repoOf(map[string]string{
		"app.1.0": pkgf(`"a" | "b"`),
		"a.1.0":   pkgf(`"c"`),
		"b.1.0":   pkgf(``),
		"c.1.0":   pkgf(``),
	})
	// Both alternatives have lag 0; fewer packages wins.
	if got, want := solve(t, r, req("app")...), map[string]string{"app": "1.0", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The error of SPEC §03.5.2: the requested version needs a package whose every matching
// version needs a newer dune than the toolchain's.
func TestUnsatisfiableExplainsToolchain(t *testing.T) {
	r := repoOf(map[string]string{
		"ocaml-lsp-server.1.26.0": pkgf(`"dune" {>= "3.0"} "dune-rpc" {>= "3.22.0"} "stdune"`),
		"ocaml-lsp-server.1.25.0": pkgf(`"dune-rpc" {< "3.22"}`),
		"dune-rpc.3.21.1":         pkgf(`"dune" {>= "3.21"} "stdune" {= version}`),
		"dune-rpc.3.22.0":         pkgf(`"dune" {>= "3.22"} "stdune" {= version}`),
		"dune-rpc.3.24.2":         pkgf(`"dune" {>= "3.24"} "stdune" {= version}`),
		"stdune.3.21.1":           pkgf(`"dune" {>= "3.21"}`),
		"stdune.3.24.2":           pkgf(`"dune" {>= "3.24"}`),
	})
	_, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, req("ocaml-lsp-server = 1.26.0"))
	var ue *UnsatError
	if !errors.As(err, &ue) || !errors.Is(err, ErrUnsatisfiable) {
		t.Fatalf("err = %v, want *UnsatError", err)
	}
	want := `cannot resolve opam packages: ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches (dune is fixed to 3.21.1 by the toolchain)`
	if err.Error() != want {
		t.Errorf("err =\n%s\nwant\n%s", err, want)
	}
	for in, want := range map[string]string{
		"nope":              "nope is not in the opam repository",
		"stdune >= 9":       `no version of stdune matches >= "9"`,
		"dune >= 4":         `the request needs dune >= "4", but dune is fixed to 3.21.1 by the toolchain`,
		"dune-rpc = 3.24.2": `dune-rpc.3.24.2 needs dune >= "3.24": dune is fixed to 3.21.1 by the toolchain`,
	} {
		_, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, req(in))
		if err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
			t.Errorf("%s: err = %v, want suffix %q", in, err, want)
		}
	}
	// Unsatisfiable only in combination (every version is fine on its own).
	r = repoOf(map[string]string{
		"a.1.0": pkgf(`"c" {= "1.0"}`),
		"b.1.0": pkgf(`"c" {= "2.0"}`),
		"c.1.0": pkgf(``),
		"c.2.0": pkgf(``),
	})
	_, err = Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, req("a", "b"))
	if !errors.Is(err, ErrUnsatisfiable) || !strings.Contains(err.Error(), "needs c = ") {
		t.Errorf("a+b: err = %v, want an UnsatError naming c", err)
	}
}

func TestLockedRecords(t *testing.T) {
	sha512 := strings.Repeat("ab", 64)
	r := repoOf(map[string]string{
		"app.1.0": "opam-version: \"2.0\"\n" +
			`depends: ["dune" "lib" {build} "tool" {post} "opt-in"]` + "\n" +
			`depopts: ["extra" "absent"]` + "\n" +
			`url { src: "https://example.org/app-1.0.tbz" checksum: ["md5=00" "sha256=` + strings.Repeat("0", 64) + `" "sha512=` + sha512 + `"] }`,
		"lib.1.0":    pkgf(``),
		"tool.1.0":   pkgf(``),
		"opt-in.1.0": pkgf(`"extra"`),
		"extra.1.0":  pkgf(``),
		"absent.1.0": pkgf(``),
		"conf-x.1":   "opam-version: \"2.0\"\nflags: conf\n",
	})
	sol, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, req("app", "conf-x"))
	if err != nil {
		t.Fatal(err)
	}
	var app, conf Locked
	var names []string
	for _, p := range sol.Packages {
		names = append(names, p.Name)
		switch p.Name {
		case "app":
			app = p
		case "conf-x":
			conf = p
		}
	}
	if want := []string{"app", "conf-x", "extra", "lib", "opt-in", "tool"}; !reflect.DeepEqual(names, want) {
		t.Errorf("packages %v, want %v (sorted, no provided, no unused depopt)", names, want)
	}
	if want := []string{"extra", "lib", "opt-in"}; !reflect.DeepEqual(app.Deps, want) {
		t.Errorf("app deps %v, want %v (no dune, no post dependency, present depopt)", app.Deps, want)
	}
	if app.URL != "https://example.org/app-1.0.tbz" || !strings.HasPrefix(app.Hash, "sha512-q6ur") || len(app.Opam) != 64 {
		t.Errorf("app record %+v", app)
	}
	if conf.URL != "" || conf.Hash != "" {
		t.Errorf("conf-x has no source, got %+v", conf)
	}
	r = repoOf(map[string]string{
		"a.1.0": "opam-version: \"2.0\"\n" + `url { src: "https://e/a.tgz" checksum: "md5=00112233445566778899aabbccddeeff" }`,
		"g.1.0": "opam-version: \"2.0\"\n" + `url { src: "git+https://e/g.git" checksum: "sha256=` + strings.Repeat("0", 64) + `" }`,
		"s.1.0": "opam-version: \"2.0\"\n" + `url { src: "https://e/s.tgz" checksum: "sha256=` + strings.Repeat("0", 64) + `" }`,
	})
	for _, root := range []string{"a", "g"} {
		_, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, req(root))
		want := "opam:" + root + ".1.0 has no sha256/sha512 checksum; bob cannot pin it"
		if !errors.Is(err, ErrNoChecksum) || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", root, err, want)
		}
	}
	sol, err = Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, req("s"))
	if err != nil || sol.Packages[0].Hash != "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("sha256 source: %+v %v", sol, err)
	}
}

func TestRepositoryHostileEntries(t *testing.T) {
	m := fstest.MapFS{
		"packages/a/a.1.0/opam":       {Data: []byte(pkgf(``))},
		"packages/a/a.2.0/opam":       {Data: []byte(`depends: [ "unterminated`)}, // parse error: unavailable
		"packages/a/a.3.0/opam":       {Data: []byte(pkgf(``)), Mode: fs.ModeSymlink},
		"packages/a/b.9.0/opam":       {Data: []byte(pkgf(``))}, // wrong directory name
		"packages/a/a.bad$ver/opam":   {Data: []byte(pkgf(``))},
		"packages/a/a.4.0/opam/x":     {Data: []byte("not a file")},
		"packages/a/a.5.0/not-opam":   {Data: []byte(pkgf(``))},
		"packages/../etc/passwd/opam": {Data: []byte("x")},
	}
	r := NewRepository(m)
	vs, err := r.Versions("a")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, v.Version)
	}
	if want := []string{"2.0", "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("versions %v, want %v", got, want)
	}
	if got := solve(t, r, req("a")...); got["a"] != "1.0" {
		t.Errorf("a = %s, want 1.0 (2.0 does not parse)", got["a"])
	}
	for _, bad := range []string{"../etc", "a/b", "", "-x", strings.Repeat("a", 200), "a b"} {
		if _, err := r.Versions(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("Versions(%q): err = %v, want ErrBadName", bad, err)
		}
		if _, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc()}, []Request{{Name: bad}}); !errors.Is(err, ErrBadName) {
			t.Errorf("Solve(%q): err = %v, want ErrBadName", bad, err)
		}
	}
	// A dependency on an invalid name is simply never satisfiable.
	r = repoOf(map[string]string{"x.1": pkgf(`"../y"`), "x.0": pkgf(``)})
	if got := solve(t, r, req("x")...); got["x"] != "0" {
		t.Errorf("x = %s, want 0", got["x"])
	}
}

func TestSolveBudgetAndCancel(t *testing.T) {
	// n independent pairs of mutually exclusive versions: a valid but large search space.
	files := map[string]string{}
	var deps []string
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("p%d.1", i)] = pkgf(fmt.Sprintf(`"q%d" {= "1"}`, i))
		files[fmt.Sprintf("p%d.2", i)] = pkgf(fmt.Sprintf(`"q%d" {= "1"}`, i))
		files[fmt.Sprintf("q%d.1", i)] = pkgf(``)
		files[fmt.Sprintf("q%d.2", i)] = pkgf(``)
		deps = append(deps, fmt.Sprintf(`"p%d"`, i))
	}
	files["root.1"] = pkgf(strings.Join(deps, " "))
	r := repoOf(files)
	sol, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc(), MaxSteps: 5}, req("root"))
	if !errors.Is(err, ErrTooComplex) && (err != nil || sol.Optimal) {
		t.Errorf("MaxSteps 5: %+v %v, want ErrTooComplex or a non-optimal solution", sol, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Solve(ctx, repoOf(files), Options{Arch: "arm64", Provided: tc()}, req("root")); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: err = %v", err)
	}
	if _, err := Solve(context.Background(), r, Options{Arch: "riscv64", Provided: tc()}, req("root")); err == nil {
		t.Errorf("unsupported arch: no error")
	}
}

func TestSolveDeterministic(t *testing.T) {
	r := repoOf(map[string]string{
		"app.1.0": pkgf(`"x" "y"`),
		"x.1.0":   pkgf(``),
		"x.2.0":   pkgf(``, `conflicts: ["y" {>= "2.0"}]`),
		"y.1.0":   pkgf(``),
		"y.2.0":   pkgf(``),
	})
	first := solve(t, r, req("app")...)
	for i := 0; i < 20; i++ {
		if got := solve(t, repoOf(map[string]string{
			"app.1.0": pkgf(`"x" "y"`),
			"x.1.0":   pkgf(``),
			"x.2.0":   pkgf(``, `conflicts: ["y" {>= "2.0"}]`),
			"y.1.0":   pkgf(``),
			"y.2.0":   pkgf(``),
		}), req("app")...); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: %v, first run %v", i, got, first)
		}
	}
}

// The real snapshot: the SPEC's example error.
func TestUnsatisfiableLSPOnSnapshot(t *testing.T) {
	repo := snapshot(t)
	_, err := Solve(context.Background(), repo, Options{Arch: "arm64", Provided: s5Toolchain()}, req("ocaml-lsp-server = 1.26.0"))
	want := `cannot resolve opam packages: ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches (dune is fixed to 3.21.1 by the toolchain)`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}

// FuzzSolve: arbitrary opam files never crash the resolver, and any solution is valid.
func FuzzSolve(f *testing.F) {
	f.Add(pkgf(`"b" {>= "2"} | "c"`), pkgf(`"c" {= version}`, `conflicts: "a"`), pkgf(`"dune" {< "4"}`, "conflict-class: \"x\""))
	f.Fuzz(func(t *testing.T, a, b, c string) {
		r := repoOf(map[string]string{"a.1": a, "b.1": b, "b.2": c, "c.2": c, "c.1": a})
		sol, err := Solve(context.Background(), r, Options{Arch: "arm64", Provided: tc(), MaxSteps: 1000}, req("a"))
		if err == nil {
			validate(t, r, "arm64", tc(), req("a"), sol)
		}
	})
}
