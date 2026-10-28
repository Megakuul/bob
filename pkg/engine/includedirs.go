package engine

import (
	"encoding/json"
	"sync"

	"github.com/megakuul/bob/pkg/engine/ir"
)

var (
	includeMu  sync.Mutex
	includeFns = map[string]func(spec json.RawMessage) string{}
)

// RegisterIncludeDir tells the engine which directory a node kind exports to its consumers as
// an include directory (cc.library's Hdrs): f returns it relative to the package directory, ""
// for none. Build errors use it to explain a header that the sandbox does not show a package
// (translate.Export). Ecosystem packages call it from init.
func RegisterIncludeDir(kind string, f func(spec json.RawMessage) string) {
	includeMu.Lock()
	defer includeMu.Unlock()
	includeFns[kind] = f
}

// IncludeDirOf returns the include directory n exports (relative to its package directory), or "".
func IncludeDirOf(n *ir.Node) string {
	includeMu.Lock()
	f := includeFns[n.Kind]
	includeMu.Unlock()
	if f == nil || len(n.Spec) == 0 {
		return ""
	}
	return f(n.Spec)
}
