//go:build bob

package only

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is a C library that restricts its platforms (label //only:only); the test narrows the list to
// the host platform and requests the other one.
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "only", cc.LibraryOpts{
		Srcs:      bob.Glob("*.c"),
		Platforms: []bob.Platform{bob.LinuxARM64, bob.LinuxAMD64},
	})
})
