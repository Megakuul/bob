package cli

import (
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// flagKind is how a flag takes its value.
type flagKind int

const (
	fBool flagKind = iota // --x, --x=true|false
	fStr                  // --x v, --x=v
	fInt                  // --x 3, --x=3
	fTwo                  // --x a b (bob graph --why)
)

// flag is one flag of a command or a global flag (SPEC §02.1).
type flag struct {
	name  string // long name without dashes, or the letter of a short flag
	kind  flagKind
	short bool // -v, -j, -C, -f, -q
}

// globalFlags are accepted by every command.
var globalFlags = []flag{
	{name: "C", kind: fStr, short: true},
	{name: "v", kind: fBool, short: true},
	{name: "vv", kind: fBool, short: true},
	{name: "q", kind: fBool, short: true},
	{name: "j", kind: fInt, short: true},
	{name: "json", kind: fBool},
	{name: "json-log", kind: fBool},
	{name: "color", kind: fStr},
	{name: "offline", kind: fBool},
	{name: "server", kind: fStr},
	{name: "help", kind: fBool},
}

// args is a parsed command line.
type args struct {
	pos   []string            // positional arguments (the subcommand word included)
	pass  []string            // everything after "--"
	dash  bool                // "--" was given
	vals  map[string][]string // flag name → values (bools: "true"/"false")
	order []string            // flags in the order given (for run.start args)
}

func (a *args) has(name string) bool { _, ok := a.vals[name]; return ok }

// bool returns the last value of a bool flag.
func (a *args) bool(name string) bool {
	v := a.vals[name]
	return len(v) > 0 && v[len(v)-1] == "true"
}

// str returns the last value of a flag, or "".
func (a *args) str(name string) string {
	v := a.vals[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// parse parses the words after the command name against the command's flags and the global
// flags. Flags may appear anywhere before "--" (SPEC §02.1).
func parse(words []string, cmdFlags []flag) (*args, error) {
	a := &args{vals: map[string][]string{}}
	all := append(append([]flag{}, globalFlags...), cmdFlags...)
	find := func(name string, short bool) (flag, bool) {
		for _, f := range all {
			if f.name == name && f.short == short {
				return f, true
			}
		}
		return flag{}, false
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--":
			a.dash = true
			a.pass = append([]string{}, words[i+1:]...)
			return a, nil
		case strings.HasPrefix(w, "--"):
			name, val, hasVal := strings.Cut(w[2:], "=")
			f, ok := find(name, false)
			if !ok {
				return nil, unknownFlag("--"+name, all)
			}
			n, err := a.take(f, "--"+name, val, hasVal, words, i)
			if err != nil {
				return nil, err
			}
			i += n
		case len(w) > 1 && w[0] == '-' && !isNumber(w):
			name := w[1:]
			val, hasVal := "", false
			if f, ok := find(name, true); ok {
				n, err := a.take(f, w, "", false, words, i)
				if err != nil {
					return nil, err
				}
				i += n
				continue
			}
			// -j4, -Cdir: a value glued to a one-letter short flag
			if f, ok := find(name[:1], true); ok && f.kind != fBool {
				val, hasVal = name[1:], true
				n, err := a.take(f, "-"+name[:1], val, hasVal, words, i)
				if err != nil {
					return nil, err
				}
				i += n
				continue
			}
			return nil, unknownFlag(w, all)
		default:
			a.pos = append(a.pos, w)
		}
	}
	return a, nil
}

// take records the value(s) of flag f found at words[i] and returns how many extra words it
// consumed.
func (a *args) take(f flag, spelled, val string, hasVal bool, words []string, i int) (int, error) {
	a.order = append(a.order, f.name)
	switch f.kind {
	case fBool:
		if !hasVal {
			val = "true"
		}
		if val != "true" && val != "false" {
			return 0, diag.New(4, "flag", spelled, "what", "true or false")
		}
		a.vals[f.name] = append(a.vals[f.name], val)
		return 0, nil
	case fTwo:
		if i+2 >= len(words) || words[i+1] == "--" || words[i+2] == "--" {
			return 0, diag.New(4, "flag", spelled, "what", "two arguments")
		}
		a.vals[f.name] = append(a.vals[f.name], words[i+1], words[i+2])
		return 2, nil
	}
	n := 0
	if !hasVal {
		if i+1 >= len(words) || words[i+1] == "--" {
			return 0, diag.New(4, "flag", spelled, "what", "a value")
		}
		val, n = words[i+1], 1
	}
	if f.kind == fInt {
		if v, err := strconv.Atoi(val); err != nil || v < 0 {
			return 0, diag.New(4, "flag", spelled, "what", "a number")
		}
	}
	a.vals[f.name] = append(a.vals[f.name], val)
	return n, nil
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// unknownFlag is BOB-E-0001 for a flag, with a suggestion within edit distance 2.
func unknownFlag(spelled string, all []flag) error {
	best, bestD := "", 3
	for _, f := range all {
		cand := "--" + f.name
		if f.short {
			cand = "-" + f.name
		}
		if d := editDistance(spelled, cand); d < bestD {
			best, bestD = cand, d
		}
	}
	if best != "" {
		return diag.New(1, "kind", "flag", "name", spelled, "suggest", best)
	}
	return diag.New(1, "kind", "flag", "name", spelled)
}
