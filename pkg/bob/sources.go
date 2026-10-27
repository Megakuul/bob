package bob

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Sources is a set of files from the repo, relative to the directory of the calling bob.go.
type Sources struct{ groups []srcGroup }

// srcGroup is the IR form of sources: glob patterns relative to the directory of a bob.go
// (resolved by bob, not by the evaluator; R-FW-021). Exact file names are escaped with "\".
type srcGroup struct {
	dir      string
	patterns []string
}

func sourcesOf(patterns []string) Sources {
	if len(patterns) == 0 {
		return Sources{}
	}
	s := bobSite()
	return Sources{groups: []srcGroup{{dir: s.dir, patterns: slices.Clone(patterns)}}}
}

// Glob selects files by patterns: "*" and "?" do not cross "/", "**" matches any depth,
// "[...]" classes as in path.Match. A pattern starting with "!" excludes.
//
//	bob.Glob("src/**/*.cpp", "!src/legacy/**")
func Glob(patterns ...string) Sources { return sourcesOf(patterns) }

// Files selects exact files (no patterns).
//
//	bob.Files("foo_test.cpp")
func Files(paths ...string) Sources {
	escaped := make([]string, len(paths))
	for i, p := range paths {
		escaped[i] = escapeGlob(p)
	}
	return sourcesOf(escaped)
}

// Dir selects a whole directory subtree.
//
//	bob.Dir("include")
func Dir(p string) Sources {
	p = path.Clean(p)
	if p == "." {
		return sourcesOf([]string{"**"})
	}
	return sourcesOf([]string{escapeGlob(p) + "/**"})
}

// escapeGlob quotes the glob metacharacters of a file name (and a leading "!").
func escapeGlob(p string) string {
	var b strings.Builder
	for i, c := range p {
		if strings.ContainsRune(`\*?[]{}`, c) || i == 0 && c == '!' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Union combines sources.
func (s Sources) Union(o ...Sources) Sources {
	all := slices.Clone(s.groups)
	for _, x := range o {
		all = append(all, x.groups...)
	}
	return Sources{groups: mergeGroups(all)}
}

// mergeGroups joins groups of one directory, keeping the first-seen order of directories and
// patterns.
func mergeGroups(gs []srcGroup) []srcGroup {
	var out []srcGroup
	idx := map[string]int{}
	for _, g := range gs {
		i, ok := idx[g.dir]
		if !ok {
			idx[g.dir] = len(out)
			out = append(out, srcGroup{dir: g.dir})
			i = len(out) - 1
		}
		for _, p := range g.patterns {
			if !slices.Contains(out[i].patterns, p) {
				out[i].patterns = append(out[i].patterns, p)
			}
		}
	}
	return out
}

// Include is a public include tree of a native library (C/C++ headers, the C header of a Rust
// staticlib): consumers get its directory on their include path through the library's typed
// handle (bob.LinkInfo), never through a path string of their own (ADR 0020 §6).
type Include struct {
	dir  string // the bob.go's label directory
	path string // as declared, relative to dir
}

// IncludeDir declares dir (relative to the bob.go directory, inside the package region; "." =
// the package directory itself, its subpackages excluded) as a public include root: consumers
// write #include <path below dir>. A dir outside the package region fails with BOB-E-1028.
//
//	Hdrs: bob.IncludeDir("include")
func IncludeDir(dir string) Include {
	if dir == "" {
		dir = "."
	}
	return Include{dir: bobSite().dir, path: dir}
}

// Dir returns the include root relative to the declaring bob.go's directory ("." for the
// directory itself, "" for the zero Include). Ecosystem packages put it on their consumers'
// include path.
func (i Include) Dir() string {
	if i.path == "" {
		return ""
	}
	return path.Clean(i.path)
}

// Sources returns the files of the include tree, for the declaring package's sources (the
// region rule R-FW-070 applies: a tree outside the package fails with BOB-E-1028 naming dir).
func (i Include) Sources() Sources {
	if i.path == "" {
		return Sources{}
	}
	p := escapeGlob(i.path)
	if path.Clean(i.path) == "." {
		p = "**"
	}
	return Sources{groups: []srcGroup{{dir: i.dir, patterns: []string{p}}}}
}

// FetchOpts declares one external file or archive.
type FetchOpts struct {
	URL     string   // https (http only with a pinned hash)
	Mirrors []string // tried after URL, in order
	Archive bool     // unpack; false = flat file
	Strip   int      // leading path components to drop (Archive)
	Name    string   // store name; default from the URL's base name
}

// fetchSpec is the IR payload of kind bob.fetch.
type fetchSpec struct {
	URL     string   `json:"url"`
	Mirrors []string `json:"mirrors"`
	Archive bool     `json:"archive"`
	Strip   int      `json:"strip"`
	Name    string   `json:"name"`
}

// treeNode is the package of a Fetch or a Step.File.
type treeNode struct{ Node }

// Fetch declares an external input pinned in bob.lock "sources" and fetched by bob, never by
// a build step. Its "out" is the file or the unpacked tree. The package name is Name, or the
// URL's base name.
//
//	var Fonts = bob.Define(func(p *bob.P) bob.Tree {
//		return bob.Fetch(p, bob.FetchOpts{URL: "https://example.org/fonts-1.2.tar.gz", Archive: true, Strip: 1})
//	})
func Fetch(p *P, o FetchOpts) Tree {
	name := o.Name
	if name == "" {
		if u, err := url.Parse(o.URL); err == nil {
			name = packageName(path.Base(u.Path))
		}
	}
	var problems []string
	for _, raw := range append([]string{o.URL}, o.Mirrors...) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
			problems = append(problems, fmt.Sprintf("%q is not an https or http URL", raw))
		}
	}
	if o.Strip < 0 {
		problems = append(problems, fmt.Sprintf("Strip %d is negative", o.Strip))
	}
	f := &treeNode{Node: newNode(p, "bob.fetch", name, nil, nil,
		fetchSpec{URL: o.URL, Mirrors: nonNil(o.Mirrors), Archive: o.Archive, Strip: o.Strip, Name: o.Name})}
	reportInvalid(&f.Node, "FetchOpts", problems)
	return f
}

// packageName turns a file name into a valid package name: lower case, other characters
// replaced by "-", leading non-alphanumerics dropped.
func packageName(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.') {
			b[i] = '-'
		}
	}
	out := strings.TrimLeft(string(b), "_-.")
	if len(out) > maxName {
		out = out[:maxName]
	}
	return out
}

// reportInvalid records BOB-E-1020 "<label>: invalid <option>: <reason>" for each problem of a
// created node.
func reportInvalid(n *Node, option string, problems []string) {
	if n.n == nil || active == nil {
		return
	}
	for _, pr := range problems {
		active.coded(diag.New(1020, "label", n.n.label.String(), "option", option, "reason", pr), n.n.label.String(), n.n.pos)
	}
}

// RunOpts declares a hermetic step. Everything the command may read is declared here.
type RunOpts struct {
	Tools   []Tool            // on PATH, in order (nixpkgs handles' Tool or bob-built tools)
	Srcs    Sources           // copied writable to $BOB_SRC (cwd)
	Inputs  map[string]Tree   // read-only at $BOB_IN_<NAME> (NAME = key upper-cased, [A-Z0-9_])
	Cmd     []string          // argv; no shell. ${VAR} expands from the step's environment only
	Env     map[string]string // extra environment
	Outputs []string          // paths relative to $BOB_OUT that must exist afterwards
	Timeout time.Duration     // 0 = 30 min
}

// runSpec is the IR payload of kind bob.run. The node's deps are the tools (first Tools
// entries, in PATH order) followed by the inputs in the order of their names; Inputs maps each
// environment name to its index in deps.
type runSpec struct {
	Cmd      []string          `json:"cmd"`
	Env      map[string]string `json:"env"`
	Tools    int               `json:"tools"`
	Inputs   map[string]int    `json:"inputs"`
	Outputs  []string          `json:"outputs"`
	TimeoutS int64             `json:"timeoutSeconds"`
}

// Run creates a sandboxed, cached build step. Its output tree is the Step (a Tree).
//
//	gen := bob.Run(p, "version-file", bob.RunOpts{
//	    Tools:   []bob.Tool{nixpkgs.Coreutils.Tool(p)},
//	    Cmd:     []string{"sh", "-c", "…"}, // only if a shell is declared as a Tool
//	    Outputs: []string{"version.txt"},
//	})
func Run(p *P, name string, o RunOpts) *Step {
	var problems []string
	deps := make([]Package, 0, len(o.Tools)+len(o.Inputs))
	for i, t := range o.Tools {
		if isNil(t) {
			nilInput(fmt.Sprintf("bob.Run %q: tool %d is nil", name, i))
			continue
		}
		deps = append(deps, t)
	}
	tools := len(deps)
	keys := make([]string, 0, len(o.Inputs))
	for k := range o.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	inputs := map[string]int{}
	for _, k := range keys {
		env := strings.ToUpper(k)
		if env == "" || strings.IndexFunc(env, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') }) >= 0 {
			problems = append(problems, fmt.Sprintf("input name %q must consist of letters, digits and '_'", k))
			continue
		}
		if _, dup := inputs[env]; dup {
			problems = append(problems, fmt.Sprintf("input names collide as $BOB_IN_%s", env))
			continue
		}
		if isNil(o.Inputs[k]) {
			nilInput(fmt.Sprintf("bob.Run %q: input %q is nil", name, k))
			continue
		}
		inputs[env] = len(deps)
		deps = append(deps, o.Inputs[k])
	}
	if len(o.Cmd) == 0 || o.Cmd[0] == "" {
		problems = append(problems, "Cmd is empty")
	}
	for _, out := range o.Outputs {
		if c := path.Clean(out); out == "" || path.IsAbs(out) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
			problems = append(problems, fmt.Sprintf("output %q must be a path inside $BOB_OUT", out))
		}
	}
	if o.Timeout < 0 {
		problems = append(problems, "Timeout is negative")
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	spec := runSpec{Cmd: nonNil(o.Cmd), Env: o.Env, Tools: tools, Inputs: inputs,
		Outputs: nonNil(o.Outputs), TimeoutS: int64(timeout / time.Second)}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	s := &Step{Node: newNode(p, "bob.run", name, deps, []Sources{o.Srcs}, spec)}
	reportInvalid(&s.Node, "RunOpts", problems)
	return s
}

// nilInput reports a nil tool or input of a step being defined (see evaluation.nilDep).
func nilInput(msg string) {
	if ev := active; ev != nil {
		if s := bobSite(); s.ok {
			msg = s.pos.String() + ": " + msg
		}
		ev.nilDep(msg)
	}
}

// Step is the result of Run; it implements Tree.
type Step struct{ Node }

// fileSpec is the IR payload of kind bob.file: one declared output of a step.
type fileSpec struct {
	Path string `json:"path"`
}

// undeclared explains why p is not one of the step's declared Outputs ("" when it is). Only
// a declared output is known to exist after the step, and a path such as "../x" would name
// something outside it (SPEC §1.4.2, R-FW-023).
func (s *Step) undeclared(p string) string {
	var spec struct {
		Outputs []string `json:"outputs"`
	}
	if err := json.Unmarshal(s.n.spec, &spec); err != nil {
		return "the step's outputs are unreadable"
	}
	if p != "" && path.Clean(p) == p && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") {
		for _, o := range spec.Outputs {
			if path.Clean(o) == p {
				return ""
			}
		}
	}
	return fmt.Sprintf("%q is not one of the step's declared Outputs %q", p, spec.Outputs)
}

// File returns one declared output of the step as a Tree.
func (s *Step) File(p string) Tree {
	if s == nil || s.n == nil {
		if active != nil {
			active.misuse(fmt.Sprintf("Step.File(%q) of a step that was not created", p))
		}
		return &treeNode{}
	}
	if why := s.undeclared(p); why != "" {
		if active != nil {
			active.misuse(fmt.Sprintf("%s: Step.File(%q): %s", s.n.label, p, why))
		}
		return &treeNode{}
	}
	name := s.n.label.Name + "." + p
	if checkName(name) != nil {
		h := sha256.Sum256([]byte(p))
		base := s.n.label.Name
		if len(base) > maxName-14 {
			base = base[:maxName-14]
		}
		name = base + ".file-" + hex.EncodeToString(h[:4])
	}
	return &treeNode{Node: newNode(s.n.evalP, "bob.file", name, []Package{s}, nil, fileSpec{Path: p})}
}
