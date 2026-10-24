package cli

import (
	"errors"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// runInit is `bob init [dir] [--lang go|cc|rust|npm|ocaml]` (A474): it starts a new project in
// dir (default: the current directory, created when missing) with the smallest working project
// of the language, the program and test of its quickstart, then locks it (bob.lock) and writes
// the native files bob generates (go.mod, Cargo.toml, package.json, …), so `bob build`, `bob
// test` and `bob run` work at once. It never overwrites a file: a directory that already holds
// bob.go or bob.lock, or any file of the template, is refused before anything is written.
func runInit(e *env) int {
	lang := e.a.str("lang")
	if lang == "" {
		lang = "go"
	}
	tmpl, ok := initTemplates[lang]
	if !ok {
		return e.out.fail(diag.New(4, "flag", "--lang", "what", "one of "+strings.Join(initLangs(), ", ")))
	}
	if len(e.a.pos) > 1 {
		return e.out.fail(diag.New(4, "flag", "bob init", "what", "at most one directory"))
	}
	dir := e.cwd
	if len(e.a.pos) == 1 {
		dir = e.a.pos[0]
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(e.cwd, dir)
		}
	}
	dir = filepath.Clean(dir)
	name := filepath.Base(dir)
	files := tmpl(initModulePath(name), initPackageName(name))
	// Checked in the outer process too, so a refusal costs no namespace entry.
	if err := initConflicts(dir, files); err != nil {
		return e.out.fail(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return e.out.fail(diag.New(7002, "reason", dir+" cannot be created"))
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	if err := initConflicts(dir, files); err != nil {
		return e.out.fail(err)
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if !e.out.json {
		e.out.printf("bob init: %s project in %s\n", lang, dir)
	}
	for _, p := range paths {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return e.out.fail(diag.New(7002, "reason", filepath.Dir(f)+" cannot be created"))
		}
		if err := writeNew(f, files[p]); err != nil {
			return e.out.fail(diag.New(7002, "reason", f+" cannot be written"))
		}
		if !e.out.json {
			e.out.printf("  + %s\n", p)
		}
	}
	// The lock (bob.lock, and the root go.mod of A271), then the native files of the template.
	res, err := build.Lock(e.ctx, build.Options{Root: dir, Patterns: []string{"//..."}, Store: st}, build.LockOptions{})
	if err != nil {
		return e.out.fail(err)
	}
	if !e.out.json {
		printChanges(e.out, res.Changes)
	}
	gen, err := build.Gen(e.ctx, build.Options{Root: dir, Store: st}, false)
	if err != nil {
		return e.out.fail(err)
	}
	if err := writeExclude(dir, gen); err != nil {
		return e.out.fail(err)
	}
	e.out.result(struct {
		Dir   string   `json:"dir"`
		Lang  string   `json:"lang"`
		Files []string `json:"files"`
	}{dir, lang, paths})
	if !e.out.json {
		cd := ""
		if dir != e.cwd {
			cd = "cd " + dir + " && "
		}
		e.out.printf("next: %sbob build && bob test && bob run %s\n", cd, initRunLabel[lang])
	}
	return e.out.finish(0, translate.Counts{}, nil)
}

// initConflicts refuses a directory that already is a project or holds a file of the template.
func initConflicts(dir string, files map[string]string) error {
	names := []string{"bob.go", "bob.lock"}
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if _, err := os.Lstat(f); err == nil {
			return diag.New(4, "flag", "bob init", "what", "a directory without "+p+" ("+f+" exists; bob init never overwrites files)")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return diag.New(7002, "reason", f+" cannot be read")
		}
	}
	return nil
}

// writeNew creates f with content; it fails when f exists.
func writeNew(f, content string) error {
	fh, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := fh.WriteString(content)
	return errors.Join(werr, fh.Close())
}

// initModulePath is the Go module path of a project named after its directory:
// "example.com/<name>" with the name reduced to the characters of a module path element.
func initModulePath(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	el := strings.Trim(b.String(), "-.")
	if el == "" {
		el = "hello"
	}
	return "example.com/" + el
}

// initPackageName is the package clause of the root bob.go (SPEC R-FW-002 R2's rule for a
// directory name: lowercased, characters outside [a-z0-9_] removed, "x" in front of an empty
// name, a leading digit or a Go keyword).
func initPackageName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	p := b.String()
	if p == "" || p[0] >= '0' && p[0] <= '9' || token.IsKeyword(p) || p == "main" {
		p = "x" + p
	}
	return p
}

func initLangs() []string {
	out := make([]string, 0, len(initTemplates))
	for l := range initTemplates {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// initRunLabel is the program each template defines.
var initRunLabel = map[string]string{
	"go": "//cmd/hello:hello", "cc": "//:hello", "rust": "//:hello", "npm": "//:hello", "ocaml": "//:hello",
}

// bobGoHead is the start of every template's bob.go: the build tag, the package and the
// nixpkgs pin (the default channel, R-NIX-005).
func bobGoHead(pkg string, imports ...string) string {
	var b strings.Builder
	b.WriteString("//go:build bob\n\npackage " + pkg + "\n\nimport (\n")
	for _, i := range imports {
		b.WriteString("\t\"github.com/megakuul/bob/pkg/" + i + "\"\n")
	}
	b.WriteString(")\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"" + lock.DefaultChannel + "\"})\n")
	return b.String()
}

// initTemplates are the projects of the quickstarts (website/content/docs/03-quickstart), keyed
// by --lang; each returns path → content for a module path and package name.
var initTemplates = map[string]func(module, pkg string) map[string]string{
	"go": func(module, pkg string) map[string]string {
		return map[string]string{
			"bob.go": bobGoHead(pkg, "bob", "golang") + `
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "` + module + `", Go: "1.26"})
})

var Hello = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/hello", golang.BinaryOpts{})
})

var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests("./...", golang.TestOpts{})
})
`,
			"cmd/hello/main.go": `package main

import "fmt"

func greeting(name string) string { return "hello, " + name }

func main() { fmt.Println(greeting("bob")) }
`,
			"cmd/hello/main_test.go": `package main

import "testing"

func TestGreeting(t *testing.T) {
	if got := greeting("bob"); got != "hello, bob" {
		t.Fatalf("got %q", got)
	}
}
`,
		}
	},
	"cc": func(_, pkg string) map[string]string {
		return map[string]string{
			"bob.go": bobGoHead(pkg, "bob", "cc", "nixpkgs") + `
var Hello = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "hello", cc.BinaryOpts{
		Srcs: bob.Files("main.cpp"),
		Deps: []cc.Linkable{nixpkgs.Lib(p, "zlib")},
	})
})

var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.NewTest(p, "hello_test", cc.BinaryOpts{
		Srcs: bob.Files("test.cpp"),
		Deps: []cc.Linkable{nixpkgs.Lib(p, "zlib")},
	})
})
`,
			"main.cpp": `#include <cstdio>
#include <zlib.h>

int main() {
    std::printf("hello from zlib %s\n", zlibVersion());
    return 0;
}
`,
			"test.cpp": `#include <cstdio>
#include <zlib.h>

int main() {
    bool ok = zlibVersion()[0] == '1';
    std::printf("TAP version 14\n1..1\n%s 1 - zlib 1.x\n", ok ? "ok" : "not ok");
    return ok ? 0 : 1;
}
`,
		}
	},
	"rust": func(_, pkg string) map[string]string {
		return map[string]string{
			"bob.go": bobGoHead(pkg, "bob", "rust") + `
var Hello = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{Name: "hello"})
})

var Test = bob.Define(func(p *bob.P) *rust.TestSet {
	return rust.Test(p, rust.TestOpts{Crate: Hello(p)})
})
`,
			"src/main.rs": `fn greeting(name: &str) -> String {
    format!("hello, {name}")
}

fn main() {
    println!("{}", greeting("bob"));
}

#[cfg(test)]
mod tests {
    #[test]
    fn greets() {
        assert_eq!(super::greeting("bob"), "hello, bob");
    }
}
`,
		}
	},
	"npm": func(_, pkg string) map[string]string {
		return map[string]string{
			"bob.go": bobGoHead(pkg, "bob", "npm") + `
var Hello = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{
		Name:  "hello",
		Entry: "src/main.ts",
		Deps:  []npm.Dependency{npm.Dep("vitest", "^3.2.0", npm.Dev())},
	})
})

var Test = bob.Define(func(p *bob.P) *npm.Test {
	return npm.NewTest(p, npm.TestOpts{Of: Hello(p)})
})
`,
			"src/greeting.ts": "export function greeting(name: string): string {\n  return `hello, ${name}`;\n}\n",
			"src/main.ts": `import { greeting } from "./greeting.js";

console.log(greeting("bob"));
`,
			"src/greeting.test.ts": `import { expect, test } from "vitest";
import { greeting } from "./greeting.js";

test("greeting", () => {
  expect(greeting("bob")).toBe("hello, bob");
});
`,
		}
	},
	"ocaml": func(_, pkg string) map[string]string {
		return map[string]string{
			"bob.go": bobGoHead(pkg, "bob", "ocaml") + `
var Greet = bob.Define(func(p *bob.P) *ocaml.Lib {
	return ocaml.Library(p, ocaml.LibOpts{Name: "greet", Srcs: bob.Files("hello.ml")})
})

var Hello = bob.Define(func(p *bob.P) *ocaml.Exe {
	return ocaml.Executable(p, ocaml.ExeOpts{Name: "hello", Srcs: bob.Files("main.ml"), Deps: []ocaml.Dep{Greet(p)}})
})

var Test = bob.Define(func(p *bob.P) *ocaml.Test {
	return ocaml.NewTest(p, ocaml.ExeOpts{Name: "hello_test", Main: "test", Srcs: bob.Files("test.ml"), Deps: []ocaml.Dep{Greet(p)}})
})
`,
			"hello.ml": "let greeting name = \"hello, \" ^ name\n",
			"main.ml":  "let () = print_endline (Greet.Hello.greeting \"bob\")\n",
			"test.ml": `let () =
  let ok = Greet.Hello.greeting "bob" = "hello, bob" in
  Printf.printf "TAP version 14\n1..1\n%s 1 - greeting\n" (if ok then "ok" else "not ok");
  if not ok then exit 1
`,
		}
	},
}
