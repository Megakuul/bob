package gen

import (
	"fmt"
	"strings"

	"github.com/megakuul/bob/pkg/ocaml/resolve"
)

// jobsMarker stands for opam's %{jobs}% in rendered commands; bob-builder replaces it with the
// job's parallelism (bop.JobsMarker).
const jobsMarker = "@@BOB_JOBS@@"

// varEnv is the variable environment opam gives one package's build: the global variables of
// SPEC §03.5.2, the package's own installation directories (below its output) and the
// variables of its dependencies (their outputs) and of the toolchain.
type varEnv struct {
	name, version string
	out           string            // the package's output (placeholder)
	deps          map[string]string // dependency name → its output path
	versions      map[string]string // every locked or provided package → version
	provided      map[string]string // toolchain package → store path of its root ("" unknown)
	ocamlLib      string            // the compiler's library directory
}

func (e *varEnv) global(v string) (string, bool) {
	switch v {
	case "os":
		return "linux", true
	case "arch":
		return opamArch(), true
	case "os-family", "os-distribution":
		return "nixos", true
	case "opam-version":
		return "2.5.1", true
	case "make":
		return "make", true
	case "jobs":
		return jobsMarker, true
	case "build", "post":
		return "true", true
	case "with-test", "with-doc", "dev", "with-dev-setup", "pinned":
		return "false", true
	case "user", "group":
		return "nixbld", true
	case "sys-ocaml-version":
		return OCamlVersion, true
	case "switch":
		return "default", true
	case "root":
		return "/build/opam-root", true
	}
	return "", false
}

// self is a directory variable of the package itself ("lib" → <out>/lib, "_:lib" → <out>/lib/<name>).
func (e *varEnv) self(v string, scoped bool) (string, bool) {
	switch v {
	case "name":
		return e.name, true
	case "version":
		return e.version, true
	case "installed":
		return "true", true
	case "enable":
		return "enable", true
	case "prefix":
		return e.out, true
	case "bin", "sbin":
		return e.out + "/" + v, true
	case "stublibs":
		return e.out + "/lib/stublibs", true
	case "toplevel":
		return e.out + "/lib/toplevel", true
	case "man":
		return e.out + "/man", true
	case "build":
		return "/build/src", true
	case "lib", "libexec", "share", "doc", "etc":
		dir := v
		if v == "libexec" {
			dir = "lib"
		}
		if scoped {
			return e.out + "/" + dir + "/" + e.name, true
		}
		return e.out + "/" + dir, true
	}
	return "", false
}

// lookup resolves an opam variable for filters and interpolation.
func (e *varEnv) lookup(name string) (string, bool) {
	pkg, v, scoped := strings.Cut(name, ":")
	if !scoped {
		if s, ok := e.self(name, false); ok {
			return s, true
		}
		return e.global(name)
	}
	if pkg == "_" || pkg == e.name {
		return e.self(v, true)
	}
	// Package variables of a combination "a+b:installed" hold when all hold.
	if strings.Contains(pkg, "+") && (v == "installed" || v == "enable") {
		all := true
		for _, p := range strings.Split(pkg, "+") {
			s, _ := e.lookup(p + ":installed")
			all = all && s == "true"
		}
		if v == "enable" {
			return map[bool]string{true: "enable", false: "disable"}[all], true
		}
		return fmt.Sprint(all), true
	}
	ver, known := e.versions[pkg]
	_, isDep := e.deps[pkg]
	root, isProvided := e.provided[pkg]
	installed := isDep || isProvided
	switch v {
	case "installed":
		return fmt.Sprint(installed), true
	case "enable":
		if installed {
			return "enable", true
		}
		return "disable", true
	case "version":
		if known && installed {
			return ver, true
		}
		return "", false
	case "native", "native-dynlink":
		if pkg == "ocaml" {
			return "true", true
		}
	case "preinstalled":
		if pkg == "ocaml" {
			return "false", true
		}
	}
	if !installed {
		return "", false
	}
	out := e.deps[pkg]
	if isProvided {
		if pkg == "ocaml" && v == "lib" {
			return e.ocamlLib, e.ocamlLib != ""
		}
		out = root
	}
	if out == "" {
		return "", false
	}
	switch v {
	case "lib", "share", "doc", "etc":
		return out + "/" + v + "/" + pkg, true
	case "bin", "sbin", "man":
		return out + "/" + v, true
	case "stublibs":
		return out + "/lib/stublibs", true
	case "prefix":
		return out, true
	case "toplevel":
		return out + "/lib/toplevel", true
	}
	return "", false
}

// commands renders an opam build:/install: field: filters applied, variables substituted.
func commands(v *resolve.Value, e *varEnv) ([][]string, error) {
	if v == nil {
		return nil, nil
	}
	items := resolve.Items(v)
	if len(items) == 0 {
		return nil, nil
	}
	if items[0].Kind != resolve.List { // a single command: [ "make" "all" ]
		items = []*resolve.Value{v}
	}
	var out [][]string
	for _, c := range items {
		if c.Kind != resolve.List {
			return nil, fmt.Errorf("line %d: a command is not a list", c.Line)
		}
		if !filterHolds(c.Opt, e) {
			continue
		}
		var argv []string
		for _, a := range c.L {
			if !filterHolds(a.Opt, e) {
				continue
			}
			switch a.Kind {
			case resolve.Str:
				argv = append(argv, resolve.Interpolate(a.S, e.lookup))
			case resolve.Ident:
				s, ok := e.lookup(a.S)
				if !ok {
					return nil, fmt.Errorf("line %d: variable %s is not defined", a.Line, a.S)
				}
				argv = append(argv, s)
			default:
				return nil, fmt.Errorf("line %d: a command argument is neither a string nor a variable", a.Line)
			}
		}
		if len(argv) > 0 {
			out = append(out, argv)
		}
	}
	return out, nil
}

// filterHolds evaluates an option list's filter; an undefined filter is false (as opam).
func filterHolds(opts []*resolve.Value, e *varEnv) bool {
	for _, o := range opts {
		if b, ok := resolve.EvalFilter(o, e.lookup); !ok || !b {
			return false
		}
	}
	return true
}
