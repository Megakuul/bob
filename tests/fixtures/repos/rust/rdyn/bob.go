//go:build bob

package rdyn

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Lib is librdyn.so (label //rdyn:rdyn).
var Lib = bob.Define(func(p *bob.P) *rust.CLib { return rust.CDylib(p, rust.LibOpts{Headers: "include"}) })
