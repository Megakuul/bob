#!/bin/bash
# Builds problem-case variants of examples/acme in tmp/variants/<name> and runs gopls (tagged) on each.
set -u
. "$(dirname "$0")/env.sh"
V=$S1/tmp/variants
rm -rf "$V"; mkdir -p "$V"
mk() { cp -r "$S1/examples/acme" "$V/$1"; sed -i "s#=> ../../fw#=> $S1/fw#" "$V/$1/go.mod"; rm -rf "$V/$1/.bob"; }
tagged='{"buildFlags":["-tags=bob"]}'
lsp() { (cd "$V/$1" && "$S1/bin/lspc" -root . -settings "$tagged" -- gopls); }

echo "## V1 flat C sources next to bob.go"
mk v1; mkdir -p "$V/v1/libs/flat"
printf '//go:build bob\n\npackage flat\n\nimport (\n\t"github.com/megakuul/bob/pkg/bob"\n\t"github.com/megakuul/bob/pkg/cc"\n)\n\nvar Lib = bob.Define(func(p *bob.P) *cc.Library {\n\treturn cc.StaticLibrary(p, "flat", cc.LibraryOpts{Srcs: bob.Glob("*.c")})\n})\n' > "$V/v1/libs/flat/bob.go"
printf 'int flat(void) { return 1; }\n' > "$V/v1/libs/flat/flat.c"; printf 'int flat(void);\n' > "$V/v1/libs/flat/flat.h"
sed -i 's#"example.com/acme/libs/foo"#"example.com/acme/libs/foo"\n\t"example.com/acme/libs/flat"#; s#rcore.Crate(p)}#rcore.Crate(p), flat.Lib(p)}#' "$V/v1/cmd/api/bob.go"
printf 'open libs/flat/bob.go\ndiag libs/flat/bob.go\ncomplete libs/flat/bob.go "cc.S"\nopen cmd/api/bob.go\ndiag cmd/api/bob.go\ncomplete cmd/api/bob.go "flat."\n' | lsp v1
echo "## V1b same, but .S/.s assembly and .cpp"
mk v1b; mkdir -p "$V/v1b/libs/asm"
printf '//go:build bob\n\npackage asm\n' > "$V/v1b/libs/asm/bob.go"; printf '.text\n' > "$V/v1b/libs/asm/start.S"; printf '.text\n' > "$V/v1b/libs/asm/x.s"
mkdir -p "$V/v1b/libs/cpp"; printf '//go:build bob\n\npackage cpp\n' > "$V/v1b/libs/cpp/bob.go"; printf 'int x;\n' > "$V/v1b/libs/cpp/x.cpp"
printf 'open libs/asm/bob.go\ndiag libs/asm/bob.go\nopen libs/cpp/bob.go\ndiag libs/cpp/bob.go\n' | lsp v1b

echo "## V2 bob.go with a different package name than the Go code in its directory"
mk v2; sed -i 's/^package greet$/package bob/' "$V/v2/internal/greet/bob.go"; sed -i '0,/^package bob$/s//package greet/' "$V/v2/internal/greet/greet.go"
printf 'open internal/greet/bob.go\ndiag internal/greet/bob.go\nopen internal/greet/greet.go\ndiag internal/greet/greet.go\nopen cmd/api/main.go\ndiag cmd/api/main.go\ncomplete cmd/api/main.go "greet."\n' | lsp v2

echo "## V3 a bob.go imports the bob.go of a main package"
mk v3; mkdir -p "$V/v3/deploy"
printf '//go:build bob\n\npackage deploy\n\nimport (\n\t"example.com/acme/cmd/api"\n\t"github.com/megakuul/bob/pkg/bob"\n\t"github.com/megakuul/bob/pkg/golang"\n)\n\nvar Image = bob.Define(func(p *bob.P) *golang.Binary { return api.Binary(p) })\n' > "$V/v3/deploy/bob.go"
printf 'open deploy/bob.go\ndiag deploy/bob.go\n' | lsp v3

echo "## V4 a bob.go identifier collides with an identifier of the real Go code"
mk v4; printf '\n// Test is real code.\nfunc Test() {}\n' >> "$V/v4/internal/greet/greet.go"
printf 'open internal/greet/greet.go\ndiag internal/greet/greet.go\nopen internal/greet/bob.go\ndiag internal/greet/bob.go\n' | lsp v4
(cd "$V/v4" && go build ./... && go vet ./... && echo "V4 untagged go build+vet: ok")

echo "## V5 directory with only external tests (package x_test) + bob.go package x"
mk v5; mkdir -p "$V/v5/itest"
printf 'package itest_test\n\nimport "testing"\n\nfunc TestX(t *testing.T) {}\n' > "$V/v5/itest/x_test.go"
printf '//go:build bob\n\npackage itest\n\nimport (\n\t"github.com/megakuul/bob/pkg/bob"\n\t"github.com/megakuul/bob/pkg/golang"\n)\n\nvar T = bob.Define(func(p *bob.P) *golang.Test { return golang.NewTest(p, "itest") })\n' > "$V/v5/itest/bob.go"
printf 'open itest/bob.go\ndiag itest/bob.go\nopen itest/x_test.go\ndiag itest/x_test.go\n' | lsp v5
(cd "$V/v5" && go vet ./... && go test ./itest/ && echo "V5 untagged vet+test: ok")

echo "## V6 import cycle that exists only under the bob tag"
mk v6; mkdir -p "$V/v6/internal/util"
printf 'package util\n\nfunc Up(s string) string { return s }\n' > "$V/v6/internal/util/util.go"
printf '//go:build bob\n\npackage util\n\nimport (\n\t"example.com/acme/internal/greet"\n\t"github.com/megakuul/bob/pkg/bob"\n\t"github.com/megakuul/bob/pkg/golang"\n)\n\nvar T = bob.Define(func(p *bob.P) *golang.Test { greet.Test(p); return golang.NewTest(p, "test") })\n' > "$V/v6/internal/util/bob.go"
sed -i 's#^// Hello#import "example.com/acme/internal/util"\n\nvar _ = util.Up\n\n// Hello#' "$V/v6/internal/greet/greet.go"
printf 'open internal/util/bob.go\ndiag internal/util/bob.go\nopen internal/greet/greet.go\ndiag internal/greet/greet.go\n' | lsp v6
