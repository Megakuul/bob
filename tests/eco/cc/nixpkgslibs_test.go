package cc

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// nixpkgsLib is one typed nixpkgs library handle exercised by TestNixpkgsLibHandles: a program
// including its headers, header-only or compiled.
type nixpkgsLib struct {
	pkg, handle, src, want string
}

var nixpkgsLibs = []nixpkgsLib{
	// boost.pc names no Cflags and no Libs; the headers live in the dev output (A564).
	{"boostasio", "nixpkgs.Boost.Lib(p)", `#include "boost/asio/io_context.hpp"
#include <boost/algorithm/string.hpp>
#include <iostream>
int main() { boost::asio::io_context io; std::string s = "boost"; boost::to_upper(s); std::cout << s << "\n"; }
`, "BOOST"},
	{"boostthread", "nixpkgs.Boost.Lib(p)", `#include <boost/thread.hpp>
#include <iostream>
int main() { boost::thread t([] { std::cout << "thread\n"; }); t.join(); }
`, "thread"},
	// headers in <dev>/include/eigen3 (named by the .pc file)
	{"eigenx", "nixpkgs.Eigen.Lib(p)", `#include <Eigen/Dense>
#include <iostream>
int main() { Eigen::Matrix2d m; m << 1, 2, 3, 4; std::cout << m.determinant() << "\n"; }
`, "-2"},
	// spdlog's headers include fmt's: a propagated package
	{"spdx", "nixpkgs.Spdlog.Lib(p)", `#include <spdlog/spdlog.h>
int main() { spdlog::info("hi"); }
`, "hi"},
	{"jsonx", "nixpkgs.NlohmannJson.Lib(p)", `#include <nlohmann/json.hpp>
#include <iostream>
int main() { nlohmann::json j = {{"a", 1}}; std::cout << j.dump() << "\n"; }
`, `{"a":1}`},
	{"fmtx", "nixpkgs.Fmt.Lib(p)", `#include <fmt/core.h>
int main() { fmt::print("fmt {}\n", 1); }
`, "fmt 1"},
	{"sslx", "nixpkgs.Openssl.Lib(p)", `#include <openssl/sha.h>
#include <cstdio>
int main() { unsigned char d[32]; SHA256((const unsigned char *)"a", 1, d); std::printf("%02x\n", d[0]); }
`, "ca"},
	{"zlibx", "nixpkgs.Zlib.Lib(p)", `#include <zlib.h>
#include <cstdio>
int main() { std::printf("%s\n", zlibVersion() != nullptr ? "zlib" : "none"); }
`, "zlib"},
	{"gtestx", "nixpkgs.Gtest.Lib(p)", `#include <gtest/gtest.h>
TEST(A, B) { EXPECT_EQ(1, 1); }
int main(int c, char **v) { testing::InitGoogleTest(&c, v); return RUN_ALL_TESTS(); }
`, "PASSED"},
}

// A nixpkgs library handle exposes its headers and libraries to the compile and link steps,
// whatever the package ships (a thin or missing .pc file, headers in a subdirectory or in a
// propagated package, header-only or compiled), and compile_commands.json agrees with the
// build: every command of it compiles in the dev shell, where clangd runs (A564). Before, a
// Boost handle built nothing: "boost/asio/io_context.hpp: No such file or directory".
func TestNixpkgsLibHandles(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if r := bobIn(t, root, home, "init", "proj", "--lang", "cc"); !ok(t, r) {
		return
	}
	dir := filepath.Join(root, "proj")
	for _, name := range []string{"main.cpp", "test.cpp"} {
		writeFile(t, filepath.Join(dir, name), "")
	}
	writeFile(t, filepath.Join(dir, "bob.go"), "//go:build bob\n\npackage proj\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})\n")
	for _, l := range nixpkgsLibs {
		writeFile(t, filepath.Join(dir, l.pkg, "main.cpp"), l.src)
		writeFile(t, filepath.Join(dir, l.pkg, "bob.go"), fmt.Sprintf(`//go:build bob

package %s

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var Main = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, %q, cc.BinaryOpts{
		Srcs: bob.Files("main.cpp"),
		Deps: []cc.Linkable{%s},
	})
})
`, l.pkg, l.pkg, l.handle))
	}
	if r := bobIn(t, dir, home, "build", "//..."); !ok(t, r) {
		return
	}
	for _, l := range nixpkgsLibs {
		r := bobIn(t, dir, home, "run", "//"+l.pkg+":"+l.pkg)
		if r.Exit != 0 || !strings.Contains(r.Stdout+r.Stderr, l.want) {
			t.Errorf("%s (%s) printed %q (exit %d), want %q", l.pkg, l.handle, r.Stdout+r.Stderr, r.Exit, l.want)
		}
	}

	// compile_commands.json and the dev shell: each command compiles.
	if r := bobIn(t, dir, home, "gen"); !ok(t, r) {
		return
	}
	var entries []struct {
		Directory string   `json:"directory"`
		File      string   `json:"file"`
		Arguments []string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "compile_commands.json"))), &entries); err != nil {
		t.Fatalf("compile_commands.json: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		var args []string
		for i := 0; i < len(e.Arguments); i++ {
			if e.Arguments[i] == "-o" && i+1 < len(e.Arguments) {
				i++
				continue
			}
			args = append(args, e.Arguments[i])
		}
		r := bobIn(t, dir, home, append([]string{"dev", "--"}, append(args, "-fsyntax-only")...)...)
		if r.Exit != 0 {
			t.Errorf("compile_commands.json: %s does not compile in the dev shell (exit %d):\n%s", e.File, r.Exit, r.Stderr)
		}
		seen[filepath.Dir(e.File)] = true
	}
	for _, l := range nixpkgsLibs {
		if !seen[l.pkg] {
			t.Errorf("compile_commands.json has no entry for %s", l.pkg)
		}
	}
}

// A nixpkgs package that ships no headers and no libraries is not a library: using it as one
// fails with a cause that says so, not with an exit status.
func TestNixpkgsLibProvidesNothing(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if r := bobIn(t, root, home, "init", "proj", "--lang", "cc"); !ok(t, r) {
		return
	}
	dir := filepath.Join(root, "proj")
	replaceIn(t, filepath.Join(dir, "bob.go"), "nixpkgs.Zlib.Lib(p)", "nixpkgs.Hello.Lib(p)")
	replaceIn(t, filepath.Join(dir, "bob.go"), "nixpkgs.Zlib.Lib(p)", "nixpkgs.Hello.Lib(p)")
	writeFile(t, filepath.Join(dir, "main.cpp"), "int main() {}\n")
	r := bobIn(t, dir, home, "build", "//:hello")
	out := r.Stdout + r.Stderr
	if r.Exit == 0 || !strings.Contains(out, "provides nothing to compile or link against") || !strings.Contains(out, "belongs in Tool") {
		t.Errorf("build exited %d with:\n%s", r.Exit, out)
	}
}
