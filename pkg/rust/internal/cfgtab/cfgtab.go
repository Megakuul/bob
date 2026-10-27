// Package cfgtab holds bob's built-in cfg tables: `rustc --print cfg --target <triple>` of the
// pinned toolchain (rustc 1.95.0), one line per entry. pkg/rust/resolve (CfgTable; lock-time
// and plan-time cfg filtering) reads them; TestRRS002aCfgTablesEqualRustc compares them with
// the toolchain. They are not build-definition API (R-FW-072, audit V20).
package cfgtab

var tables = map[string][]string{
	"aarch64-unknown-linux-gnu": {
		`debug_assertions`, `panic="unwind"`, `target_abi=""`, `target_arch="aarch64"`, `target_endian="little"`,
		`target_env="gnu"`, `target_family="unix"`, `target_feature="neon"`, `target_has_atomic="128"`,
		`target_has_atomic="16"`, `target_has_atomic="32"`, `target_has_atomic="64"`, `target_has_atomic="8"`,
		`target_has_atomic="ptr"`, `target_os="linux"`, `target_pointer_width="64"`, `target_vendor="unknown"`, `unix`,
	},
	"x86_64-unknown-linux-gnu": {
		`debug_assertions`, `panic="unwind"`, `target_abi=""`, `target_arch="x86_64"`, `target_endian="little"`,
		`target_env="gnu"`, `target_family="unix"`, `target_feature="fxsr"`, `target_feature="sse"`,
		`target_feature="sse2"`, `target_has_atomic="16"`, `target_has_atomic="32"`, `target_has_atomic="64"`,
		`target_has_atomic="8"`, `target_has_atomic="ptr"`, `target_os="linux"`, `target_pointer_width="64"`,
		`target_vendor="unknown"`, `unix`,
	},
}

// Lookup returns a copy of the table of triple; ok is false for an unknown triple.
func Lookup(triple string) ([]string, bool) {
	t, ok := tables[triple]
	if !ok {
		return nil, false
	}
	return append([]string(nil), t...), true
}
