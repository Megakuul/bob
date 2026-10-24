---
title: Multi-language projects
section: Guides
order: 3
summary: Link a C++ library and a Rust static library into a Go program through cgo, in one repository and one build graph.
---

This guide builds one program from three languages: a Go binary that calls a C++ library
(which uses OpenSSL from nixpkgs) and a Rust static library through cgo. You will see how
libraries cross language boundaries as typed values, and where to declare what.

The same patterns work in other directions: Rust crates can link C libraries (`Native`), C
programs can link Rust (`rust.StaticLib`, `rust.CDylib`) and Go (`Module.CArchive`), and OCaml
libraries can link C (`Native`). The repository fixture `tests/fixtures/repos/polyglot` adds
protobuf code generation and a Svelte frontend to the example below.

## The project

```text
polyglot/
├── go.mod, bob.go            the Go module, the cgo declaration and the binary
├── libs/foo/                 C++ library libfoo.so (OpenSSL)
├── crates/rcore/             Rust static library librcore.a
└── cmd/api/main.go           Go program using both through cgo
```

```bash
mkdir polyglot && cd polyglot
git init
```

## The C++ library

```c file=libs/foo/include/foo.h
#pragma once
#ifdef __cplusplus
extern "C" {
#endif
/* Writes the lowercase hex SHA-256 of msg into out (65 bytes incl. NUL). Returns 0 on success. */
int foo_sha256_hex(const char *msg, char out[65]);
#ifdef __cplusplus
}
#endif
```

```cpp file=libs/foo/src/foo.cpp
#include "foo.h"
#include <openssl/evp.h>
#include <string>
#include <cstdio>

extern "C" int foo_sha256_hex(const char *msg, char out[65]) {
    unsigned char md[EVP_MAX_MD_SIZE];
    unsigned int len = 0;
    std::string s(msg);
    if (EVP_Digest(s.data(), s.size(), md, &len, EVP_sha256(), nullptr) != 1) return 1;
    for (unsigned int i = 0; i < len; i++) std::snprintf(out + 2 * i, 3, "%02x", md[i]);
    out[64] = '\0';
    return 0;
}
```

```go file=libs/foo/bob.go
//go:build bob

package foo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is libfoo.so (label //libs/foo:foo).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
		Srcs: bob.Glob("src/*.cpp"),
		Hdrs: "include",
		Std:  cc.CXX20,
		Deps: []cc.Linkable{nixpkgs.Lib(p, "openssl")},
	})
})
```

## The Rust static library

`rust.StaticLib` builds `librcore.a` and exports the `Headers` directory to C consumers:

```c file=crates/rcore/include/rcore.h
#pragma once
#include <stdint.h>
#include <stddef.h>
int64_t rcore_num_hex(uint64_t n, char *out, size_t cap);
```

```rust file=crates/rcore/src/lib.rs
//! Small Rust staticlib used from Go via cgo.
use std::ffi::c_char;

/// Formats `n` with itoa and hex-encodes the result into `out` (NUL-terminated).
/// Returns the number of bytes written (without NUL) or -1 if `out` is too small.
#[no_mangle]
pub extern "C" fn rcore_num_hex(n: u64, out: *mut c_char, cap: usize) -> i64 {
    let mut buf = itoa::Buffer::new();
    let encoded = hex::encode(buf.format(n));
    if out.is_null() || encoded.len() + 1 > cap {
        return -1;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(encoded.as_ptr(), out as *mut u8, encoded.len());
        *out.add(encoded.len()) = 0;
    }
    encoded.len() as i64
}
```

```go file=crates/rcore/bob.go
//go:build bob

package rcore

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Crate is the rcore staticlib (label //crates/rcore:rcore).
var Crate = bob.Define(func(p *bob.P) *rust.CLib {
	return rust.StaticLib(p, rust.LibOpts{
		Name:    "rcore",
		Headers: "include",
		Deps:    []rust.Dep{rust.Crate("itoa", "1"), rust.Crate("hex", "0.4")},
	})
})
```

## The Go program

The cgo preamble includes both headers. bob puts their include directories on the cgo
compiler's path and links the libraries at the final link:

```go file=cmd/api/main.go
// Command api prints the SHA-256 of "bob" (C++/OpenSSL) and 42 hex-encoded (Rust).
package main

/*
#include <stdlib.h>
#include "foo.h"
#include "rcore.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func sha256Hex(s string) string {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	var out [65]C.char
	if C.foo_sha256_hex(cs, &out[0]) != 0 {
		return ""
	}
	return C.GoString(&out[0])
}

func numHex(n uint64) string {
	var out [64]C.char
	if C.rcore_num_hex(C.uint64_t(n), &out[0], C.size_t(len(out))) < 0 {
		return ""
	}
	return C.GoString(&out[0])
}

func main() {
	fmt.Printf("digest=%s numhex=%s\n", sha256Hex("bob"), numHex(42))
}
```

`Module.Cgo` gives the cgo package its libraries. They are typed: `foo.Lib(p)` and
`rcore.Crate(p)` are both `cc.Linkable`, and passing something that is not a library does not
compile:

```go file=bob.go
//go:build bob

package polyglot

import (
	"example.com/polyglot/crates/rcore"
	"example.com/polyglot/libs/foo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Module is the Go module example.com/polyglot (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/polyglot", Go: "1.26"})
})

// FFI gives the cgo code of cmd/api its C++ and Rust libraries.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return Module(p).Cgo("./cmd/api", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
})

// API is the cgo binary (label //cmd/api:api). It calls FFI first: a binary picks up the
// cgo declarations evaluated before it.
var API = bob.Define(func(p *bob.P) *golang.Binary {
	FFI(p)
	return Module(p).Binary("./cmd/api", golang.BinaryOpts{})
})
```

```text file=go.mod
module example.com/polyglot

go 1.26.0
```

> [!NOTE]
> The binary is declared in the root `bob.go`, not in `cmd/api/bob.go`, because `cmd/api` is
> a `main` package: other `bob.go` files (for example one that builds an image of the binary)
> could not import it. Calling `FFI(p)` inside `API` makes sure the cgo declaration is part of
> every build of the binary.

## Build and run

```bash run
bob lock
bob build
```

```text expect
…
built   //cmd/api:api
built   //crates/rcore:rcore
built   //libs/foo:foo
…
```

```bash run
bob run //cmd/api:api
```

```text expect
digest=81b637d8fcd2c6da6359e6963113a1170de795e4b725b84d1e0b4cfd9ec58ce9 numhex=3432
```

`bob graph --why //cmd/api:api nixpkgs:openssl` shows how OpenSSL got into the program:

```bash run
bob graph --why //cmd/api:api nixpkgs:openssl
```

```text expect
//cmd/api:api                   bob.go:…
└─ //cmd/api:go                 bob.go:…
   └─ //libs/foo:foo            libs/foo/bob.go:…
      └─ nixpkgs:openssl        libs/foo/bob.go:…
```

The path goes through `//cmd/api:go`, the cgo package that `Module.Cgo` declared.

## Who owns which file

| Language | bob writes for tooling | You keep |
|---|---|---|
| Go | `go.mod`, `go.sum` | Go code, `bob.go` |
| C/C++ | `compile_commands.json` | sources, headers |
| Rust | `Cargo.toml` files, `Cargo.lock`, `.cargo/config.toml` | `src/`, `build.rs` |
| npm | `package.json`, `tsconfig.json`, `node_modules` | sources, `vite.config.*`, `svelte.config.*` |
| OCaml | `.merlin` | `.ml`/`.mli` files |

## Cross-compiling

Go, C/C++ and Rust packages build for the other architecture with
`--platform linux-amd64` (or `linux-arm64`); bob uses nixpkgs' cross toolchains. Libraries from
nixpkgs come from the target system's own package set by default, which is usually in the
binary cache; `Libs: cc.CrossLibs` takes them from the cross package set instead (one glibc in
the closure, usually built from source). OCaml packages build for the host only
(`BOB-E-0011`). `bob test --platform linux-amd64` on an arm64 machine runs the tests under
qemu-user emulation; the result line names the runner, for example
`PASS    //greet:go_test (0.2s, qemu-x86_64)`. A test that cannot run on the machine is reported
as `skipped`; `--require-all-tests` turns that into a failure.

## Next steps

- [Generated code](04-generated-code.md): add protobuf, GraphQL or OpenAPI.
- [bob.go reference](01-bob-go-reference.md).
- Ship the program: [Images, tarballs and AppImages](../03-quickstart/07-outputs.md).
