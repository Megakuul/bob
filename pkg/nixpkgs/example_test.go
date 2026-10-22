//go:build bob

package nixpkgs_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

func ExampleTool() {
	_ = bob.Define(func(p *bob.P) *bob.Step {
		return bob.Run(p, "schema", bob.RunOpts{
			Tools:   []bob.Tool{nixpkgs.Tool(p, "protobuf")},
			Cmd:     []string{"protoc", "--version"},
			Outputs: []string{"version.txt"},
		})
	})
}

func ExampleLib() {
	_ = bob.Define(func(p *bob.P) *nixpkgs.Package {
		return nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))
	})
}

func ExamplePkg() {
	_ = bob.Define(func(p *bob.P) *nixpkgs.Package {
		return nixpkgs.Pkg(p, "tzdata")
	})
}

func ExampleExe() {
	_ = bob.Define(func(p *bob.P) bob.Executable {
		return nixpkgs.Exe(p, "curl", "bin/curl")
	})
}
