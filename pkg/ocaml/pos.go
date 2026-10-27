package ocaml

import (
	"fmt"
	"path"
	"runtime"
	"runtime/debug"
	"strings"
)

// callerPos is the "<dir>/bob.go:<line>" position of the running bob.go call ("" outside a
// bob.go of this build), for messages that point at a handle's declaration (ocaml.Indirect).
func callerPos() string {
	bi, found := debug.ReadBuildInfo()
	if !found || bi.Main.Path == "" {
		return ""
	}
	var pcs [64]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if path.Base(f.File) == "bob.go" {
			if rel, ok := strings.CutPrefix(f.File, bi.Main.Path+"/"); ok {
				return fmt.Sprintf("%s:%d", rel, f.Line)
			}
			return fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if !more {
			return ""
		}
	}
}
