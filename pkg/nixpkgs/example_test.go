//go:build bob

package nixpkgs_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

func ExampleAttr_Tool() {
	_ = bob.Define(func(p *bob.P) *bob.Step {
		return bob.Run(p, "schema", bob.RunOpts{
			Tools:   []bob.Tool{nixpkgs.Protobuf.Tool(p)},
			Cmd:     []string{"protoc", "--version"},
			Outputs: []string{"version.txt"},
		})
	})
}

func ExampleAttr_Lib() {
	_ = bob.Define(func(p *bob.P) *nixpkgs.Package {
		return nixpkgs.Openssl.Lib(p, nixpkgs.Modules("libcrypto"))
	})
}

func ExampleAttr_Pkg() {
	_ = bob.Define(func(p *bob.P) *nixpkgs.Package {
		return nixpkgs.Tzdata.Pkg(p)
	})
}

func ExampleAttr_Exe() {
	_ = bob.Define(func(p *bob.P) bob.Executable {
		return nixpkgs.Curl.Exe(p)
	})
}

func ExampleOutputs() {
	_ = bob.Define(func(p *bob.P) *nixpkgs.Package {
		return nixpkgs.Openssl.Lib(p, nixpkgs.Outputs(nixpkgs.Out, nixpkgs.OutDev))
	})
}
