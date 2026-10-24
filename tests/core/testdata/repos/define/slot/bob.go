//go:build bob

package slot

import "github.com/megakuul/bob/pkg/bob"

// Prog is bound by leaf/bob.go (R-FW-071 typed slot).
var Prog = bob.NewSlot[*bob.Step]()

// Unbound is never bound: Get must fail with BOB-E-1020.
var Unbound = bob.NewSlot[*bob.Step]()
