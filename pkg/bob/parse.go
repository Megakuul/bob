package bob

import "github.com/megakuul/bob/pkg/bob/diag"

// Ref is the typed result of Parse: a Label, a Pattern or a Configured value; String() returns
// the canonical form (SPEC §1.3, R-FW-015).
type Ref interface{ String() string }

// Parse accepts the union of label, pattern and configured forms (R-FW-015) and returns a typed
// value. Use ParseLabel when only a label is valid.
func Parse(s string) (Ref, error) { return nil, diag.NotImplemented("bob.Parse") }
