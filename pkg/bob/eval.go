package bob

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

// IRVersion is the version of the graph IR the evaluator writes (SPEC §1.6).
const irVersion = 1

// active is the running evaluation; nil before Main starts (package initialisation).
var active *evaluation

// evaluation is one run of the registered constructors for the requested configurations.
type evaluation struct {
	reg          *registry
	env          *environment
	hostPlatform Platform
	root         string // repository root (for reading bob.go lines in messages)
	ps           map[string]*P
	stack        []frame
	nodes        map[string]*node
	diags        []diagnostic
	calls        map[Position]string // bob.go position → function called there (column.go)
	lines        map[string][][]byte // bob.go file → its lines, read for columns
}

type frame struct {
	def *definition
	p   *P
}

// diagnostic is one evaluation error. defs attributes it to definitions: it is reported only if
// one of them is relevant for the requested targets. An error without defs is always reported.
type diagnostic struct {
	code  diag.Code // 0 = no code of the SPEC table
	pos   Position
	label string
	msg   string
	chain []string // labels from a requested package to the one the error is about
	defs  []*definition
}

func (ev *evaluation) top() *frame {
	if len(ev.stack) == 0 {
		return nil
	}
	return &ev.stack[len(ev.stack)-1]
}

// errorf records a definition error of d (nil = not attributable, always reported).
func (ev *evaluation) errorf(d *definition, format string, args ...any) {
	dg := diagnostic{msg: fmt.Sprintf(format, args...)}
	if d != nil {
		dg.defs = []*definition{d}
	}
	ev.diags = append(ev.diags, dg)
}

// misuse records an error of the constructor that is running. A message that starts with
// "<file>:<line>: " (ext.Errorf's convention, from Node.DefinedAt) carries that position, and
// a following "//<label>: " that label.
func (ev *evaluation) misuse(msg string) {
	dg := diagnostic{msg: msg}
	if top := ev.top(); top != nil {
		dg.defs = []*definition{top.def}
	}
	dg.pos, dg.label = misusePos(msg)
	ev.diags = append(ev.diags, dg)
}

// misusePosRe matches the "<file>.go:<line>: " position and the optional "//<dir>:<name>: "
// label at the start of a misuse message.
var misusePosRe = regexp.MustCompile(`^([^\s:]+\.go):([0-9]+): (?:(//[^\s:]*:[^\s:]+): )?`)

func misusePos(msg string) (Position, string) {
	m := misusePosRe.FindStringSubmatch(msg)
	if m == nil {
		return Position{}, ""
	}
	line, err := strconv.Atoi(m[2])
	if err != nil || line <= 0 {
		return Position{}, ""
	}
	return Position{File: m[1], Line: line}, m[3]
}

// coded records an error with a code of the SPEC table, attributed to the running constructor.
func (ev *evaluation) coded(e *diag.Error, label string, pos Position) {
	dg := diagnostic{code: e.Code, label: label, pos: pos, msg: e.Error()}
	if top := ev.top(); top != nil {
		dg.defs = []*definition{top.def}
	}
	ev.diags = append(ev.diags, dg)
}

// cycle records BOB-E-1006 for the constructors on chain, which call each other for p. The
// labels of the chain are known only after the constructors finished, so the message is built
// at the end of the evaluation (finishCycles).
func (ev *evaluation) cycle(chain []frame, p *P) {
	dg := diagnostic{code: 1006}
	for _, f := range chain {
		dg.defs = append(dg.defs, f.def)
	}
	dg.label = p.id // resolved by finishCycles
	ev.diags = append(ev.diags, dg)
}

func (ev *evaluation) finishCycles() {
	for i := range ev.diags {
		dg := &ev.diags[i]
		if dg.code != 1006 || dg.msg != "" {
			continue
		}
		p := ev.ps[dg.label]
		var parts []string
		for _, d := range append(slices.Clone(dg.defs), dg.defs[0]) {
			name := varRef(d)
			if ns := d.created[p]; len(ns) > 0 {
				name = ns[len(ns)-1].label.String()
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", name, d.pos))
		}
		dg.label = ""
		if ns := dg.defs[0].created[p]; len(ns) > 0 {
			dg.label = ns[len(ns)-1].label.String()
		}
		dg.pos = dg.defs[0].pos
		dg.msg = diag.New(1006, "cycle", strings.Join(parts, " → ")).Error()
	}
}

// config returns the one P of a configuration.
func (ev *evaluation) config(plat Platform, v Variant) *P {
	v = v.normal()
	id := configString(plat, v)
	if p, ok := ev.ps[id]; ok {
		return p
	}
	p := &P{ev: ev, platform: plat, variant: v, id: id}
	ev.ps[id] = p
	return p
}

// request is what the generated evaluator passes to Main as its only argument (JSON).
//
//	{"targets":["//...","//api:api@linux-amd64+debug"],"host":"linux-arm64","root":"."}
//
// A target is a label or pattern, optionally followed by "@<platform>[+variant...]"; without it
// the target is built for the host in release. host defaults to the evaluator's own
// architecture, root (the repository root, for messages) to the working directory.
type request struct {
	Targets []string `json:"targets"`
	Host    string   `json:"host"`
	Root    string   `json:"root"`
}

// target is one parsed request target.
type target struct {
	text    string
	pattern Pattern
	p       *P
}

// Main is called by the generated evaluator. bob.go files never call it.
//
// Protocol (SPEC R-FW-043.6): the request is os.Args[1] (see request); the IR JSON goes to
// fd 3; diagnostics go to stderr, one per line as
//
//	bob:: {"type":"diag","severity":"error","code":"BOB-E-1006","label":"//a:b","file":"a/bob.go","line":3,"msg":"…"}
//
// ("code", "label", "file" and "line" only when known). Main never exits the process: an
// evaluation succeeded if and only if an IR document was written to fd 3.
func Main() {
	req := request{Targets: []string{"//..."}}
	var reqErr error
	if len(os.Args) > 1 {
		dec := json.NewDecoder(strings.NewReader(os.Args[1]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			reqErr = fmt.Errorf("evaluator request: %w", err)
		}
	}
	var ir []byte
	var diags []diagnostic
	if reqErr != nil {
		diags = []diagnostic{{msg: reqErr.Error()}}
	} else {
		ir, diags = run(reg, env(), req)
	}
	writeDiags(os.Stderr, diags)
	if ir == nil {
		return
	}
	if _, err := os.NewFile(3, "ir").Write(ir); err != nil {
		if !errors.Is(err, syscall.EBADF) {
			writeDiags(os.Stderr, []diagnostic{{msg: "writing the graph to fd 3: " + err.Error()}})
			return
		}
		// Started by hand (no fd 3): print the graph.
		_, _ = os.Stdout.Write(append(ir, '\n'))
	}
}

func writeDiags(w io.Writer, diags []diagnostic) {
	bw := bufio.NewWriter(w)
	for _, d := range diags {
		line := map[string]any{"type": "diag", "severity": "error", "msg": d.msg}
		if d.code != 0 {
			line["code"] = d.code.ID()
		}
		if d.label != "" {
			line["label"] = d.label
		}
		if d.pos.File != "" {
			line["file"], line["line"] = d.pos.File, d.pos.Line
		}
		if len(d.chain) > 0 {
			line["chain"] = d.chain
		}
		b, err := json.Marshal(line)
		if err != nil {
			continue
		}
		fmt.Fprintf(bw, "bob:: %s\n", b)
	}
	_ = bw.Flush()
}

// hostOf maps the evaluator's architecture to its platform.
func hostOf(goarch string) Platform {
	if goarch == "amd64" {
		return LinuxAMD64
	}
	return LinuxARM64
}

// run evaluates r's definitions for req and returns the canonical IR, or the errors that are
// relevant for the requested targets (then the IR is nil).
func run(r *registry, e *environment, req request) ([]byte, []diagnostic) {
	ev := &evaluation{reg: r, env: e, hostPlatform: hostOf(runtime.GOARCH), root: req.Root,
		ps: map[string]*P{}, nodes: map[string]*node{}}
	if req.Host != "" {
		ev.hostPlatform = Platform(req.Host)
		if !slices.Contains(platforms, ev.hostPlatform) {
			return nil, []diagnostic{{msg: fmt.Sprintf("evaluator request: unknown host platform %q", req.Host)}}
		}
	}
	if ev.root == "" {
		ev.root = "."
	}
	// Global errors: registration problems and calls before evaluation.
	var global []diagnostic
	for _, msg := range graph.Problems() {
		global = append(global, diagnostic{msg: msg})
	}
	global = append(global, r.early...)
	repo, named, errs := checkRepo(r)
	global = append(global, errs...)
	pipes, errs := checkPipelines(r)
	global = append(global, errs...)

	targets, errs := ev.parseTargets(req.Targets, named)
	global = append(global, errs...)
	if len(errs) > 0 {
		return nil, global
	}

	active = ev
	defer func() { active = nil }()
	defs := slices.Clone(r.defs)
	sort.SliceStable(defs, func(i, j int) bool { return posLess(defs[i].pos, defs[j].pos) })
	ps := make([]*P, 0, len(ev.ps))
	for _, p := range ev.ps {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].id < ps[j].id })
	for _, p := range ps {
		for _, d := range defs {
			d.get(p)
		}
	}
	ev.finishCycles()

	roots, relevant, errs := ev.selectRoots(targets, defs)
	global = append(global, errs...)
	global = append(global, checkPlatforms(roots)...)
	for _, dg := range ev.diags {
		if len(dg.defs) == 0 {
			global = append(global, dg)
			continue
		}
		for _, d := range dg.defs {
			if relevant[d] {
				global = append(global, dg)
				break
			}
		}
	}
	if global = dedupe(global); len(global) > 0 {
		return nil, global
	}
	ir, err := ev.encode(repo, pipes, targets, roots)
	if err != nil {
		return nil, []diagnostic{{msg: "encoding the graph: " + err.Error()}}
	}
	return ir, nil
}

func posLess(a, b Position) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Line < b.Line
}

func dedupe(ds []diagnostic) []diagnostic {
	seen := map[string]bool{}
	var out []diagnostic
	for _, d := range ds {
		k := d.pos.String() + "\x00" + d.msg
		if !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	return out
}

// parseTargets parses the request targets and creates their configurations.
func (ev *evaluation) parseTargets(texts []string, named map[string]Named) ([]target, []diagnostic) {
	var out []target
	var errs []diagnostic
	names := make([]string, 0, len(named))
	for n := range named {
		names = append(names, n)
	}
	for _, s := range texts {
		pat, cfg, hasCfg := strings.Cut(s, "@")
		if err := Pattern(pat).Valid(); err != nil {
			errs = append(errs, diagnostic{msg: err.Error()})
			continue
		}
		var v Variant
		var plat Platform
		if hasCfg {
			var err error
			if v, plat, err = parseConfig(cfg, names); err == nil {
				err = checkNamed(v, names)
			}
			if err != nil {
				errs = append(errs, diagnostic{msg: fmt.Sprintf("target %q: %v", s, err)})
				continue
			}
		}
		if plat == "" {
			plat = ev.hostPlatform
		}
		out = append(out, target{text: s, pattern: Pattern(pat), p: ev.config(plat, ev.applyNamed(v, named))})
	}
	return out, errs
}

// applyNamed applies the named variants of v (in alphabetical order) on top of its built-in
// tokens.
func (ev *evaluation) applyNamed(v Variant, named map[string]Named) Variant {
	out := v.normal()
	for _, n := range out.Named {
		named[n].apply(&out)
	}
	out.Named = v.normal().Named
	return out.normal()
}

// selectRoots finds the roots of the targets, the nodes reachable from them and the definitions
// whose errors matter: those that created a reachable node, the constructors they called, and
// for wildcard targets every definition below the pattern's directory.
func (ev *evaluation) selectRoots(targets []target, defs []*definition) ([]*node, map[*definition]bool, []diagnostic) {
	var roots []*node
	var errs []diagnostic
	relevant := map[*definition]bool{}
	for _, t := range targets {
		found := false
		for _, n := range ev.nodes {
			if n.evals[t.p] && t.pattern.Match(n.label) {
				roots = append(roots, n)
				found = true
			}
		}
		k, dir, l := t.pattern.kind()
		for _, d := range defs {
			if d.mod != "" {
				continue
			}
			switch {
			case k == patAll,
				k == patBelow && (d.dir == dir || strings.HasPrefix(d.dir, dir+"/")),
				k == patDir && d.dir == dir,
				k == patLabel && !found && l.Module == "" && l.Nix == "" && d.dir == l.Dir:
				relevant[d] = true
			}
		}
		if !found && k != patAll {
			errs = append(errs, diagnostic{code: 3, msg: diag.New(3, "pattern", t.text).Error()})
		}
	}
	seen := map[*node]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, d := range n.defs {
			relevant[d] = true
		}
		for _, dep := range n.deps {
			walk(dep)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	var mark func(d *definition)
	done := map[*definition]bool{}
	mark = func(d *definition) {
		if done[d] {
			return
		}
		done[d] = true
		relevant[d] = true
		for c := range d.calls {
			mark(c)
		}
	}
	for _, d := range keys(relevant) {
		mark(d)
	}
	return roots, relevant, errs
}

// checkPlatforms reports every node reachable from roots that is built for a platform its kind
// does not allow (ext.Restrict, R-FW-030, R-PLAT-006), with the shortest chain from a root.
func checkPlatforms(roots []*node) []diagnostic {
	parent := map[*node]*node{}
	seen := map[*node]bool{}
	queue := slices.Clone(roots)
	sort.Slice(queue, func(i, j int) bool { return queue[i].id < queue[j].id })
	for _, r := range queue {
		seen[r] = true
	}
	var out []diagnostic
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n.allowed != nil && !slices.Contains(n.allowed, n.p.platform) {
			var chain []string
			for c := n; c != nil; c = parent[c] {
				chain = append(chain, c.label.String())
			}
			slices.Reverse(chain)
			allowed := make([]string, len(n.allowed))
			for i, p := range n.allowed {
				allowed[i] = string(p)
			}
			e := diag.New(6, "label", n.label.String(), "platform", string(n.p.platform), "platforms", strings.Join(allowed, ", "))
			out = append(out, diagnostic{code: 6, pos: n.pos, label: n.label.String(), msg: e.Error(), chain: chain})
			continue // its dependencies are not built for it
		}
		for _, d := range n.deps {
			if !seen[d] {
				seen[d] = true
				parent[d] = n
				queue = append(queue, d)
			}
		}
	}
	return out
}

// keys returns the keys of m (a copy, so the caller may add to m while ranging).
func keys(m map[*definition]bool) []*definition {
	out := make([]*definition, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	return out
}

var varLine = regexp.MustCompile(`^\s*(?:var\s+)?([\pL_][\pL\pN_]*)\b[^=]*=`)

// varName reads the variable name of a definition from its bob.go line ("var Lib = bob.Define(").
func varName(d *definition) string {
	if d.mod != "" || d.pos.File == "" {
		return ""
	}
	root := "."
	if active != nil {
		root = active.root
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.pos.File)))
	if err != nil {
		return ""
	}
	lines := bytes.Split(b, []byte("\n"))
	if d.pos.Line < 1 || d.pos.Line > len(lines) {
		return ""
	}
	if m := varLine.FindSubmatch(lines[d.pos.Line-1]); m != nil {
		return string(m[1])
	}
	return ""
}
