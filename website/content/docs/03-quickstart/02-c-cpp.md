---
title: C and C++ quickstart
section: Quickstart
order: 2
summary: Build a C++ static library that links zlib from nixpkgs, a program that uses it, and a TAP test.
---

In this quickstart you build a C++ library and a program with bob. The library links `zlib`
from nixpkgs, and a small test checks it. You also get a `compile_commands.json` for clangd
and a dev shell with GCC and clang tools.

You need [bob installed](../02-install/02-install-bob.md). You do not need a compiler: bob
uses GCC from nixpkgs.

## The project

```text
hello-cc/
├── go.mod                      needed to evaluate bob.go (see the Go quickstart)
├── bob.go                      nixpkgs pin and dev shell
├── libs/greet/
│   ├── bob.go                  libgreet.a and its test
│   ├── include/greet/greet.hpp
│   ├── src/greet.cpp
│   └── test/greet_test.cpp
└── app/
    ├── bob.go                  bin/hello
    └── main.cpp
```

```bash
mkdir hello-cc && cd hello-cc
git init
```

## Write the code

`libs/greet/include/greet/greet.hpp` is the public header:

```cpp file=libs/greet/include/greet/greet.hpp
#pragma once
#include <string>

namespace greet {
// hello returns a greeting for name.
std::string hello(const std::string& name);
// packed_size returns the zlib-compressed size of s.
unsigned long packed_size(const std::string& s);
}
```

`libs/greet/src/greet.cpp` uses zlib:

```cpp file=libs/greet/src/greet.cpp
#include "greet/greet.hpp"

#include <zlib.h>

namespace greet {
std::string hello(const std::string& name) { return "hello, " + name; }

unsigned long packed_size(const std::string& s) {
    uLongf n = compressBound(s.size());
    std::string out(n, '\0');
    if (compress(reinterpret_cast<Bytef*>(out.data()), &n,
                 reinterpret_cast<const Bytef*>(s.data()), s.size()) != Z_OK) {
        return 0;
    }
    return n;
}
}
```

`libs/greet/test/greet_test.cpp` prints [TAP](https://testanything.org) so bob can report each
case. Any program works as a test: exit status 0 means pass.

```cpp file=libs/greet/test/greet_test.cpp
#include <cstdio>

#include "greet/greet.hpp"

int main() {
    bool ok = greet::hello("bob") == "hello, bob";
    std::printf("TAP version 14\n1..1\n%s 1 - hello\n", ok ? "ok" : "not ok");
    return ok ? 0 : 1;
}
```

`app/main.cpp`:

```cpp file=app/main.cpp
#include <cstdio>
#include <string>

#include "greet/greet.hpp"

int main(int argc, char** argv) {
    std::string name = argc > 1 ? argv[1] : "world";
    std::string msg = greet::hello(name);
    std::printf("%s (%lu bytes packed)\n", msg.c_str(), greet::packed_size(msg));
    return 0;
}
```

## Describe the build

The library and its test, in `libs/greet/bob.go`. `PrivDeps` links zlib without exposing its
headers to users of `greet`; `Deps` would propagate them:

```go file=libs/greet/bob.go
//go:build bob

package greet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is libgreet.a (label //libs/greet:greet). It compresses with zlib from nixpkgs.
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "greet", cc.LibraryOpts{
		Srcs:     bob.Glob("src/*.cpp"),
		Hdrs:     "include",
		Std:      cc.CXX20,
		PrivDeps: []cc.Linkable{nixpkgs.Lib(p, "zlib")},
	})
})

// Test runs test/greet_test.cpp (label //libs/greet:greet_test).
var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.NewTest(p, "greet_test", cc.BinaryOpts{
		Srcs: bob.Files("test/greet_test.cpp"),
		Std:  cc.CXX20,
		Deps: []cc.Linkable{Lib(p)},
	})
})
```

The program, in `app/bob.go`. It imports the library's `bob.go` package by its path below the
module path of the root `go.mod`:

```go file=app/bob.go
//go:build bob

package app

import (
	"example.com/hellocc/libs/greet"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// App is bin/hello (label //app:hello).
var App = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "hello", cc.BinaryOpts{
		Srcs: bob.Glob("*.cpp"),
		Std:  cc.CXX20,
		Deps: []cc.Linkable{greet.Lib(p)},
	})
})
```

The root `bob.go` pins nixpkgs and declares a dev shell with GCC and the clang tools (clangd,
clang-format, clang-tidy). The dev shell does not add a C toolchain by itself yet:

```go file=bob.go
//go:build bob

package hellocc

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Dev is the default development shell (bob dev, bob env).
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{
		Packages: []bob.Tool{nixpkgs.Tool(p, "gcc"), nixpkgs.Tool(p, "clang-tools")},
	})
})
```

And the `go.mod` every bob repository needs today (it names the import path of your `bob.go`
packages):

```text file=go.mod
module example.com/hellocc

go 1.26.0
```

## Lock, build, test, run

```bash run
bob lock
```

```text expect
bob.lock: 2 changes
  + cc       cc
  + nixpkgs  nixpkgs   …
```

For C and C++, `bob.lock` holds the nixpkgs revision (which fixes GCC and zlib) and the
downloads of `cc.External` packages, if you have any.

```bash run
bob build
```

```text expect
built   //app:hello
built   //libs/greet:greet
bob: 2 built
```

```bash run
bob test
```

```text expect
PASS    //libs/greet:greet_test (…s)
bob: 1 passed, 0 failed, 0 cached
```

```bash run
bob run //app:hello -- bob
```

```text expect
hello, bob (18 bytes packed)
```

## Editor support

bob writes `compile_commands.json` at the repository root, with the exact compiler flags of
every source file. clangd reads it. Generate it with `bob gen` (the first build writes it
too):

```bash run
bob gen
head -c 200 compile_commands.json
```

```text expect
[
  {
    "directory": "…",
    "file": "app/main.cpp",
```

Start an editor or a shell in the dev environment to get GCC and clangd on `PATH`:

```bash run
bob dev -- sh -c 'gcc --version | head -1; command -v clangd'
```

```text expect
bob: dev shell default (gcc 15.3.0, clang-tools …) · …
gcc (GCC) 15.3.0
/nix/store/…-clang-tools-…/bin/clangd
```

With the [shell hook](../04-guides/05-dev-environments.md#the-shell-hook), editors started from
the project directory find `clangd` through `.bob/bin` without `bob dev`.

## Third-party C libraries

Two ways to use a C or C++ library you do not build yourself:

- **From nixpkgs:** `nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))`. bob links the
  pkg-config modules you name (or all of the package's modules).
- **From source, with its own build system:** `cc.External` downloads an archive (pinned in
  `bob.lock`) and builds it with CMake, Autotools, Meson or Make:

```go verify
//go:build bob

package zstd

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Zstd is upstream zstd built with its own Makefile (label //third_party/zstd:zstd).
var Zstd = bob.Define(func(p *bob.P) *cc.ExternalLib {
	return cc.External(p, "zstd", cc.ExternalOpts{
		URL:   "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
		Build: cc.Make,
		Args:  []string{"-C", "lib"},
		Libs:  []string{"zstd"},
	})
})
```

Both are `cc.Linkable`, so they go into `Deps` or `PrivDeps` like your own libraries.

## Next steps

- [Multi-language projects](../04-guides/03-multi-language.md): call this library from Go or Rust.
- All C/C++ options: [bob.go reference](../04-guides/01-bob-go-reference.md#c-and-c).
- Ship it: [Images, tarballs and AppImages](07-outputs.md).
