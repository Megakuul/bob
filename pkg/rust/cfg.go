package rust

import "github.com/megakuul/bob/pkg/bob/diag"

// CfgTable returns bob's built-in cfg set of a target triple, one `rustc --print cfg` line per
// entry (SPEC R-RS-002: the tables equal the pinned toolchain's output). Not implemented yet.
func CfgTable(triple string) ([]string, error) {
	return nil, diag.NotImplemented("rust.CfgTable")
}
