---
title: C and C++ quickstart
section: Quickstart
order: 2
summary: Build a C++ program that links zlib from nixpkgs, with a small test and editor support through compile_commands.json.
---

In this quickstart you build a C++ program that links `zlib` from nixpkgs, and a test. You
also get `compile_commands.json` for clangd and a dev shell with GCC.

You need [bob installed](../02-install/02-install-bob.md). You do not need a compiler: bob
uses GCC from nixpkgs.

## Start the project

```bash
mkdir hello-cc && cd hello-cc
git init
```

Let bob write the project, lock it and write the files it generates (it never overwrites a
file):

```bash run
bob init --lang cc
```

```text expect
bob init: cc project in …
  + bob.go
  + main.cpp
  + test.cpp
bob.lock: 2 changes
…
next: bob build && bob test && bob run //:hello
```

```tree
📁 hello-cc/
  📄 bob.go
  📄 bob.lock
  📄 main.cpp
  📄 test.cpp
```

## The code

`main.cpp` prints the zlib version it was linked with:

```cpp
#include <cstdio>
#include <zlib.h>

int main() {
    std::printf("hello from zlib %s\n", zlibVersion());
    return 0;
}
```

`test.cpp` is a test: exit status 0 means pass. Output in [TAP](https://testanything.org)
format is reported per case:

```cpp
#include <cstdio>
#include <zlib.h>

int main() {
    bool ok = zlibVersion()[0] == '1';
    std::printf("TAP version 14\n1..1\n%s 1 - zlib 1.x\n", ok ? "ok" : "not ok");
    return ok ? 0 : 1;
}
```

## The build description

```go verify
//go:build bob

package hellocc

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

var Hello = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "hello", cc.BinaryOpts{
		Srcs: bob.Files("main.cpp"),
		Deps: []cc.Linkable{nixpkgs.Zlib.Lib(p)},
	})
})

var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.NewTest(p, "hello_test", cc.BinaryOpts{
		Srcs: bob.Files("test.cpp"),
		Deps: []cc.Linkable{nixpkgs.Zlib.Lib(p)},
	})
})
```

- `nixpkgs.Zlib.Lib(p)` is zlib from your nixpkgs pin, linked into the program. Every nixpkgs
  package is a typed Go value (`nixpkgs.Zlib`, `nixpkgs.Openssl`, …): your editor completes the
  names and shows each package's version and licence, and a misspelt name does not compile.
  bob adds its headers and links it.

### Header-only libraries and libraries without a pkg-config file

Every nixpkgs library handle (`nixpkgs.Boost.Lib(p)`, `nixpkgs.Eigen.Lib(p)`,
`nixpkgs.Spdlog.Lib(p)`, `nixpkgs.NlohmannJson.Lib(p)`, …) gives your program its headers and
its libraries, whatever the package ships:

- headers are found in the include directory of the package's dev output, in the directories its
  `.pc` file names (Eigen's `include/eigen3`) and in those of the packages it propagates (spdlog
  brings the headers of fmt);
- a header-only library (Boost.Asio, nlohmann_json, glm) has no libraries and adds none;
- a package whose `.pc` file names no libraries (Boost's does not) is linked with every library
  of its lib output, as far as the program uses it (`--as-needed`), so `boost::thread` works
  without naming `-lboost_thread`.

The build, clangd (through `compile_commands.json`) and the dev shell resolve the flags with the
same code, so what compiles in `bob build` also compiles in your editor. A package that ships no
headers and no libraries at all, such as a program (`nixpkgs.Hello`), is not a library: bob fails
with `the nixpkgs library hello-… provides nothing to compile or link against`. Use
`nixpkgs.Hello.Tool(p)` for programs.

bob generates the repository's `go.mod` itself (the `bob.go` files are Go code); you do not
write one.

## Build, test, run

For C and C++, the nixpkgs revision in `bob.lock` fixes GCC and zlib.

```bash run
bob build
```

```text expect
built   //:hello
…
bob: 1 built
```

```bash run
bob test
```

```text expect
PASS    //:hello_test (…s)
bob: 1 passed, 0 failed, 0 cached
```

```bash run
bob run //:hello
```

```text expect
hello from zlib 1.…
```

## Editor support

bob writes `compile_commands.json` with the exact compiler flags of every file, for clangd.
`bob gen` writes it without building (the first build writes it too):

```bash run
bob gen
grep -c '"file"' compile_commands.json
```

```text expect
2
```

### Why there is a `zz_bob_cgo.go`

`bob.go` is a Go file, so the directory is a Go package that also holds `main.cpp`. The Go
tools refuse that: `go vet` and gopls report `C++ source files not allowed when not using cgo
or SWIG` and then show nothing for `bob.go`. bob therefore writes `zz_bob_cgo.go` (build tags
`bob && cgo`, one line: `import "C"`), which tells the Go tools that the directory uses cgo and
may hold C and C++ files. The file is generated (it carries no code), is not committed and is
invisible to bob's own builds. It needs a C compiler on the machine running gopls, which the dev
shell's `gcc` provides when gopls runs from `.bob/bin` (see [Editors](../04-guides/05-dev-environments.md#editors)).

GCC and the clang tools (clangd, clang-format) of your nixpkgs pin are on `PATH` inside the
dev shell, without declaring anything:

```bash run
bob dev -- sh -c 'gcc --version | head -1'
```

```text expect
bob: dev shell default (gcc 15.3.0, clang-tools …) · …
gcc (GCC) 15.3.0
```

## Libraries of your own

A real project splits code into libraries, one per directory, each with its own `bob.go`:
`cc.StaticLibrary` and `cc.SharedLibrary` build `lib<name>.a`/`.so` with a public include
directory (`Hdrs: bob.IncludeDir("include")`), and other packages list them in `Deps`. A
`bob.go` names only files of its own directory; headers of another library reach you only
through `Deps`. Third-party projects with their own build system (CMake, Autotools, Meson,
Make) come in through `cc.External`. See the
[bob.go reference](../04-guides/01-bob-go-reference.md#c-and-c).

## Next steps

- [Multi-language projects](../04-guides/03-multi-language.md): call C++ from Go or Rust.
- Ship it: [Images, tarballs and AppImages](07-outputs.md).
