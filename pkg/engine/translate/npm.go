package translate

import (
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// npm runtime failures (SPEC §03.4.4, §02 E-SCRIPT-DENIED = BOB-E-2013, E-PHANTOM-DEP =
// BOB-E-2014). Node and tsc report them with the store path of the package that failed; bob
// names that package and the bob.go change that fixes it. They are recognised in build logs
// (a Vite build, a test run, tsc) and in the stderr of a program bob run started.
const (
	cScriptDenied diag.Code = 2013
	cPhantomDep   diag.Code = 2014
)

// Program explains a program that bob run started and that exited non-zero, from the last lines
// of its stderr: nil when nothing in them is a failure bob knows how to fix. label is the
// package bob run built.
func Program(label string, stderr []string) *diag.Error {
	f := npmFailure(stderr)
	if f == nil {
		return nil
	}
	e := newErr(f.code, f.args(label))
	for _, l := range f.excerpt {
		e.Excerpt = append(e.Excerpt, storePathRe().ReplaceAllStringFunc(l, func(m string) string {
			return "…/" + m[len("/nix/store/")+33:]
		}))
	}
	return e
}

// npmLogError explains a build log that shows an npm runtime failure; nil when it shows none.
func (t *Translator) npmLogError(drv string, lines []string) *diag.Error {
	f := npmFailure(lines)
	if f == nil {
		return nil
	}
	label := "nixpkgs " + drvName(drv)
	if p, ok := t.ctx.Packages[drv]; ok {
		label = p.Label
	}
	e := t.pkgErr(f.code, drv, f.args(label))
	e.Excerpt = f.excerpt
	return e
}

// npmFail is one recognised failure: code, the npm package at fault and (2014) the package it
// uses without declaring it.
type npmFail struct {
	code    diag.Code
	pkg     string
	dep     string
	excerpt []string
}

func (f *npmFail) args(label string) map[string]string {
	if f.code == cScriptDenied {
		return map[string]string{"label": label, "package": f.pkg, "script": "install", "reason": "native addon"}
	}
	return map[string]string{"label": label, "package": f.pkg, "dep": f.dep}
}

// npmFailure finds the first npm runtime failure in lines (bounded: the translator keeps at
// most a head and a tail of every log).
func npmFailure(lines []string) *npmFail {
	for i, l := range lines {
		switch {
		case strings.Contains(l, "Could not locate the bindings file"):
			// bindings lists the paths it tried (" → <path>"); the first names the package
			// whose addon was never built. Its own frames (npm-bindings) do not count.
			for _, x := range lines[i+1 : min(len(lines), i+16)] {
				if !strings.Contains(x, "→") {
					continue
				}
				if pkg := storePackage(x); pkg != "" && pkg != "bindings" {
					return &npmFail{code: cScriptDenied, pkg: pkg, excerpt: []string{strings.TrimSpace(l)}}
				}
			}
		case strings.Contains(l, "ERR_MODULE_NOT_FOUND") || strings.Contains(l, "Cannot find module '") ||
			strings.Contains(l, "Cannot find package '") || strings.Contains(l, "error TS2307:"):
			if f := phantom(lines, i); f != nil {
				return f
			}
		}
	}
	return nil
}

// phantom reads an unresolved import at lines[i]: Node's ESM message (Cannot find package 'X'
// imported from <file>), its CommonJS one (Cannot find module 'X' + Require stack: - <file>)
// or tsc's (<file>(l,c): error TS2307: Cannot find module 'X' …). It is E-PHANTOM-DEP when X is
// a package name and <file> lies inside an npm package in the store.
func phantom(lines []string, i int) *npmFail {
	l := lines[i]
	dep := quoted(l, "Cannot find package '")
	if dep == "" {
		dep = quoted(l, "Cannot find module '")
	}
	dep = packageOf(dep)
	if dep == "" {
		return nil
	}
	from := ""
	switch {
	case strings.Contains(l, "error TS2307:"):
		from = l[:strings.Index(l, "error TS2307:")]
	case strings.Contains(l, " imported from "):
		from = l[strings.Index(l, " imported from "):]
	default:
		for j, x := range lines[i+1 : min(len(lines), i+8)] {
			if strings.HasPrefix(strings.TrimSpace(x), "Require stack:") && i+j+2 < len(lines) {
				from = lines[i+j+2]
				break
			}
		}
	}
	pkg := storePackage(from)
	if pkg == "" || pkg == dep {
		return nil
	}
	return &npmFail{code: cPhantomDep, pkg: pkg, dep: dep, excerpt: []string{strings.TrimSpace(l)}}
}

// quoted returns the text between prefix and the next quote in l.
func quoted(l, prefix string) string {
	_, rest, ok := strings.Cut(l, prefix)
	if !ok {
		return ""
	}
	s, _, ok := strings.Cut(rest, "'")
	if !ok {
		return ""
	}
	return s
}

// packageOf is the package a bare import specifier names ("@a/b/c" → "@a/b", "x/y" → "x"); ""
// for relative, absolute, node: and invalid specifiers.
func packageOf(spec string) string {
	if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.Contains(spec, ":") ||
		strings.ContainsAny(spec, " \t\\") || len(spec) > 214 {
		return ""
	}
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 || parts[0] == "@" || parts[1] == "" {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// storePackage is the npm package a file inside the store belongs to: the name after the last
// node_modules/ of the first store path in s (/nix/store/<hash>-<name>/… or the translator's
// …/<name>/… form); "" when s names no file of a store package.
func storePackage(s string) string {
	start := strings.Index(s, "/nix/store/")
	skip := len("/nix/store/")
	if start < 0 {
		if start = strings.Index(s, "…/"); start < 0 {
			return ""
		}
		skip = len("…/")
	}
	p := s[start+skip:]
	if end := strings.IndexAny(p, " \t'\"()"); end >= 0 {
		p = p[:end]
	}
	i := strings.LastIndex(p, "node_modules/")
	if i < 0 {
		return ""
	}
	rest := strings.Split(p[i+len("node_modules/"):], "/")
	if len(rest) < 2 { // the file must be inside the package directory
		return ""
	}
	name := rest[0]
	if strings.HasPrefix(name, "@") {
		if len(rest) < 3 {
			return ""
		}
		name += "/" + rest[1]
	}
	if name == ".bin" || name == "." || name == ".." {
		return ""
	}
	return name
}
