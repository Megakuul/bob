// Package nixext is the extension API of bob's own ecosystem packages for nixpkgs (layer L1,
// ADR 0020 §2): the Attr type and its role methods (Tool, Lib, Pkg, Exe), the options, the
// typed toolchain packages (Toolchain) and New for computed attributes (cross compilers,
// versioned toolchains, members of language package sets).
//
// Build definitions use package nixpkgs, which re-exports these types and holds the generated
// handles (nixpkgs.Openssl); a bob.go cannot import nixext (R-FW-002 R7). Ecosystem packages
// import nixext rather than nixpkgs so they do not compile the generated surface.
package nixext
