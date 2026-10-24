// Package nixpkgs exposes the packages of nixpkgs as typed bob tools and dependencies.
//
// Every top-level package of nixpkgs at bob's default pin is a generated handle of type Attr,
// named after its attribute (openssl → Openssl, go_1_26 → Go1_26, nix-update → NixUpdate,
// 3proxy → N3proxy). Find one with your editor's completion (nixpkgs.Op… → Openssl) or
// `go doc nixpkgs.Openssl`; the doc comment names the version, description and licence. The
// role is a method: Tool (build time), Lib (linked), Pkg (run time), Exe (a program).
//
// Not in the surface: package sets and scopes (python3Packages, ocamlPackages, pkgsCross, …),
// aliases, broken and insecure packages, packages available on neither aarch64-linux nor
// x86_64-linux, and packages that fail to evaluate (cmd/nixpkgsgen/exclude.json). Nested
// attributes that bob needs are listed in cmd/nixpkgsgen/include.json (rust_1_97's rustc is
// Rust1_97PackagesStableRustc). The handles are generated for the pin bob's new locks use
// (nixext.SurfaceRev); with another pin, a package the pin lacks fails before any build
// with BOB-E-1001.
package nixpkgs

//go:generate go run ../../cmd/nixpkgsgen gen
