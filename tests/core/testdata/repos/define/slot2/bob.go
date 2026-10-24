//go:build bob

package slot2

import "github.com/megakuul/bob/pkg/bob"

// Prog is bound twice (a/bob.go and b/bob.go): a definition error.
var Prog = bob.NewSlot[*bob.Step]()
