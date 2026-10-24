package rust

import (
	"fmt"

	"github.com/megakuul/bob/pkg/rust/internal/cfgtab"
)

// CfgTable returns bob's built-in cfg set of a target triple, one `rustc --print cfg` line per
// entry (SPEC R-RS-002a: the tables equal the pinned toolchain's output). bob filters
// platform-specific crate dependencies and sets the build scripts' CARGO_CFG_* from it.
func CfgTable(triple string) ([]string, error) {
	t, ok := cfgtab.Lookup(triple)
	if !ok {
		return nil, fmt.Errorf("rust: no cfg table for target %q (bob knows aarch64-unknown-linux-gnu and x86_64-unknown-linux-gnu)", triple)
	}
	return t, nil
}
