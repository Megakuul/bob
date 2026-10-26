package translate

import (
	"strings"
	"testing"
)

const h32 = "0123456789abcdfghijklmnpqrsvwxyz"

// SPEC §03.4.4 runtime translations: a denied install script (bindings cannot find the addon)
// is BOB-E-2013, an undeclared import inside a store package is BOB-E-2014, both rendered with
// the SPEC's ✗ line; anything else is left alone.
func TestProgramNpmFailures(t *testing.T) {
	bindings := []string{
		"/nix/store/" + h32 + "-npm-bindings-1.5.0/node_modules/bindings/bindings.js:126",
		"  err = new Error(",
		"        ^",
		"",
		"Error: Could not locate the bindings file. Tried:",
		" → /nix/store/" + h32 + "-npm-better-sqlite3-12.11.1/node_modules/better-sqlite3/build/better_sqlite3.node",
		" → /nix/store/" + h32 + "-npm-better-sqlite3-12.11.1/node_modules/better-sqlite3/build/Debug/better_sqlite3.node",
		"    at bindings (/nix/store/" + h32 + "-npm-bindings-1.5.0/node_modules/bindings/bindings.js:126:9)",
	}
	esm := []string{
		"node:internal/modules/esm/resolve:873",
		"Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'react' imported from /nix/store/" + h32 + "-npm-@scope+ui-1.0.0/node_modules/@scope/ui/dist/index.js",
		"  code: 'ERR_MODULE_NOT_FOUND'",
	}
	cjs := []string{
		"Error: Cannot find module 'debug/src/node'",
		"Require stack:",
		"- /nix/store/" + h32 + "-npm-express-4.0.0/node_modules/express/lib/router.js",
	}
	tsc := []string{
		"../../nix/store/" + h32 + "-npm-plugin-1.0.0/node_modules/plugin/index.d.ts(3,20): error TS2307: Cannot find module 'vite' or its corresponding type declarations.",
	}
	mapped := []string{"…/npm-plugin-1.0.0/node_modules/plugin/index.d.ts(3,20): error TS2307: Cannot find module 'vite' or its corresponding type declarations."}
	user := []string{"Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'zod' imported from /home/u/app/src/main.js"}
	relative := []string{"Error: Cannot find module './gen.js'", "Require stack:", "- /nix/store/" + h32 + "-npm-x-1.0.0/node_modules/x/a.js"}

	cases := []struct {
		name  string
		lines []string
		code  int
		first string
		hint  string
	}{
		{"bindings", bindings, 2013,
			`✗ better-sqlite3 needs its install script (native addon). Add npm.AllowScripts(<handle of better-sqlite3>) to //native:native`,
			`declare better-sqlite3 (npm.Dep or npm.Indirect("better-sqlite3")) and add npm.AllowScripts(<its handle>) to //native:native`},
		{"esm", esm, 2014, `✗ @scope/ui uses react without declaring it. Add npm.Extend(<handle of @scope/ui>, npm.Peer(<handle of react>))`, ""},
		{"cjs", cjs, 2014, `✗ express uses debug without declaring it. Add npm.Extend(<handle of express>, npm.Peer(<handle of debug>))`, ""},
		{"tsc", tsc, 2014, `✗ plugin uses vite without declaring it. Add npm.Extend(<handle of plugin>, npm.Peer(<handle of vite>))`, ""},
		{"mapped", mapped, 2014, `✗ plugin uses vite without declaring it. Add npm.Extend(<handle of plugin>, npm.Peer(<handle of vite>))`, ""},
		{"user source", user, 0, "", ""},
		{"relative", relative, 0, "", ""},
		{"nothing", []string{"TypeError: x is not a function"}, 0, "", ""},
	}
	for _, c := range cases {
		e := Program("//native:native", c.lines)
		if c.code == 0 {
			if e != nil {
				t.Errorf("%s: got %s, want nil", c.name, e.Code.ID())
			}
			continue
		}
		if e == nil || int(e.Code) != c.code {
			t.Errorf("%s: got %v, want BOB-E-%d", c.name, e, c.code)
			continue
		}
		var b strings.Builder
		Block(&b, e, e.Hint(), Style{})
		lines := strings.Split(b.String(), "\n")
		if lines[0] != c.first {
			t.Errorf("%s: first line %q, want %q", c.name, lines[0], c.first)
		}
		if c.hint != "" && !strings.Contains(b.String(), "\n  hint: "+c.hint+"\n") {
			t.Errorf("%s: block lacks hint %q:\n%s", c.name, c.hint, b.String())
		}
		if strings.Contains(b.String(), "/nix/store/") {
			t.Errorf("%s: block leaks a store path:\n%s", c.name, b.String())
		}
	}
}
