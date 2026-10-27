package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The generated files, relative to the module root.
const (
	attrsFile    = "pkg/nixpkgs/attrs_gen.go"
	mainProgFile = "pkg/nixpkgs/nixext/mainprog_gen.go"
	surfaceFile  = "pkg/nixpkgs/nixext/surface_gen.go"
)

const maxDescription = 160

type include struct {
	Attr string `json:"attr"`
	Why  string `json:"why"`
}

type exclude struct {
	Pattern string `json:"pattern,omitempty"` // path.Match pattern over top-level attribute names
	Class   string `json:"class,omitempty"`   // a class of the snapshot facts (documentation; always applied)
	Why     string `json:"why"`
}

// classes are the exclusion classes gen applies from the snapshot facts (ADR 0020 §2);
// exclude.json documents each with its reason.
var classes = []string{"not-derivation", "alias", "broken", "insecure", "unavailable", "eval-failure"}

func readInclude(root string) ([]include, error) {
	var inc []include
	if err := readJSON(filepath.Join(root, "cmd", "nixpkgsgen", "include.json"), &inc); err != nil {
		return nil, err
	}
	for _, i := range inc {
		if i.Attr == "" || i.Why == "" {
			return nil, fmt.Errorf("include.json: every entry needs attr and why")
		}
	}
	return inc, nil
}

func readExclude(root string) ([]exclude, error) {
	var ex []exclude
	if err := readJSON(filepath.Join(root, "cmd", "nixpkgsgen", "exclude.json"), &ex); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, e := range ex {
		if (e.Pattern == "") == (e.Class == "") || e.Why == "" {
			return nil, fmt.Errorf("exclude.json: every entry needs either pattern or class, and why")
		}
		if _, err := path.Match(e.Pattern, ""); err != nil {
			return nil, fmt.Errorf("exclude.json: pattern %q: %w", e.Pattern, err)
		}
		have[e.Class] = true
	}
	for _, c := range classes {
		if !have[c] {
			return nil, fmt.Errorf("exclude.json: class %q is not documented", c)
		}
	}
	return ex, nil
}

func readJSON(file string, v any) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return nil
}

// handle is one generated variable.
type handle struct {
	name, attr string
	info       Info
}

// surface is the result of selecting the snapshot's attributes.
type surface struct {
	rev     string
	handles []handle // sorted by Go name
	groups  map[string][]string
}

// excluded reports why the attribute is left out ("" = included).
func excluded(attr string, inf Info, ex []exclude) string {
	for _, e := range ex {
		if e.Pattern != "" {
			if ok, _ := path.Match(e.Pattern, attr); ok {
				return "pattern " + e.Pattern
			}
		}
	}
	switch {
	case inf.Failed:
		return "eval-failure"
	case !inf.Drv:
		return "not-derivation"
	case inf.Alias:
		return "alias"
	case inf.Broken:
		return "broken"
	case inf.Insecure:
		return "insecure"
	case len(inf.Platforms) == 0:
		return "unavailable"
	case !inf.EvalOK:
		return "eval-failure"
	}
	return ""
}

// selectSurface applies exclude.json and include.json to the snapshot.
func selectSurface(s *Snapshot, ex []exclude, inc []include) (*surface, error) {
	var attrs []string
	for _, a := range s.Names {
		inf, ok := s.Attrs[a]
		if !ok {
			return nil, fmt.Errorf("snapshot: no facts for %s", a)
		}
		if excluded(a, inf, ex) == "" {
			attrs = append(attrs, a)
		}
	}
	for _, i := range inc {
		inf, ok := s.Attrs[i.Attr]
		if !ok {
			return nil, fmt.Errorf("include.json: %s is not in the snapshot; run nixpkgsgen snapshot", i.Attr)
		}
		if why := excluded(i.Attr, inf, nil); why != "" {
			return nil, fmt.Errorf("include.json: %s cannot be a handle (%s)", i.Attr, why)
		}
		attrs = append(attrs, i.Attr)
	}
	sort.Strings(attrs)
	names, groups := assignNames(attrs)
	sf := &surface{rev: s.Rev, groups: groups}
	for _, a := range attrs {
		sf.handles = append(sf.handles, handle{name: names[a], attr: a, info: s.Attrs[a]})
	}
	sort.Slice(sf.handles, func(i, j int) bool { return sf.handles[i].name < sf.handles[j].name })
	return sf, nil
}

// defaultRev reads lock.DefaultRev from pkg/engine/lock/nixpkgs.go: the generator does not
// import the lock package, which would link the network and cgo into -check.
func defaultRev(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "pkg", "engine", "lock", "nixpkgs.go"))
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`(?m)^const DefaultRev = "([0-9a-f]{40})"$`).FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("pkg/engine/lock/nixpkgs.go has no const DefaultRev = \"<40 hex>\"")
	}
	return string(m[1]), nil
}

// generate returns the generated files (path relative to root → content).
func generate(root, snapFile string) (map[string][]byte, error) {
	if snapFile == "" {
		rev, err := defaultRev(root)
		if err != nil {
			return nil, err
		}
		snapFile = snapshotPath(root, rev)
	}
	s, err := readSnapshot(snapFile)
	if err != nil {
		return nil, err
	}
	ex, err := readExclude(root)
	if err != nil {
		return nil, err
	}
	inc, err := readInclude(root)
	if err != nil {
		return nil, err
	}
	sf, err := selectSurface(s, ex, inc)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for file, src := range map[string][]byte{attrsFile: sf.attrsGo(), mainProgFile: sf.mainProgGo(), surfaceFile: sf.surfaceGo()} {
		b, err := format.Source(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		out[file] = b
	}
	return out, nil
}

func header(rev string) string {
	return "// Code generated by nixpkgsgen from nixpkgs " + rev + ". DO NOT EDIT.\n\n"
}

func (sf *surface) attrsGo() []byte {
	var b bytes.Buffer
	b.WriteString(header(sf.rev))
	b.WriteString("// The handles of the typed nixpkgs surface (ADR 0020 §2, R-NIX-031): one per top-level\n")
	b.WriteString("// package of the snapshot minus cmd/nixpkgsgen/exclude.json, plus cmd/nixpkgsgen/include.json.\n")
	fmt.Fprintf(&b, "// %d handles.\n", len(sf.handles))
	if len(sf.groups) > 0 {
		b.WriteString("//\n// Name collisions (every member uses the A_<attr> fallback name):\n")
		var keys []string
		for k := range sf.groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "//   %s: %s\n", orNone(k), strings.Join(sf.groups[k], ", "))
		}
	}
	b.WriteString("\npackage nixpkgs\n\nimport \"github.com/megakuul/bob/pkg/nixpkgs/nixext\"\n\nvar (\n")
	for _, h := range sf.handles {
		for _, l := range docLines(h) {
			b.WriteString("\t// " + l + "\n")
		}
		fmt.Fprintf(&b, "\t%s = nixext.New(%s)\n", h.name, strconv.Quote(h.attr))
	}
	b.WriteString(")\n")
	return b.Bytes()
}

func orNone(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

// docLines is the doc comment of a handle (ADR 0020 §2).
func docLines(h handle) []string {
	ver := strings.Join(strings.Fields(clean(h.info.Version)), "-")
	if ver == "" {
		ver = "unversioned"
	}
	desc := strings.TrimRight(cut(clean(h.info.Description), maxDescription), ". ")
	if desc == "" {
		desc = h.attr + " " + ver
	}
	lic := "unknown"
	if len(h.info.Licenses) > 0 {
		var ls []string
		for _, l := range h.info.Licenses {
			if l = clean(l); l != "" {
				ls = append(ls, l)
			}
		}
		if len(ls) > 0 {
			lic = strings.Join(ls, ", ")
		}
	}
	lines := []string{fmt.Sprintf("%s is %s %s: %s. Licence: %s.", h.name, h.attr, ver, desc, lic)}
	if h.info.Unfree {
		lines = append(lines, "Unfree: needs nixpkgs.AllowUnfree().")
	}
	if len(h.info.Platforms) == 1 {
		lines = append(lines, "Only on "+h.info.Platforms[0]+".")
	}
	return lines
}

// clean drops control characters and collapses white space.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// cut shortens s to at most n bytes on a word boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	t := s[:n]
	for !utf8.ValidString(t) {
		t = t[:len(t)-1]
	}
	if i := strings.LastIndexByte(t, ' '); i > 0 {
		t = t[:i]
	}
	return strings.TrimRight(t, " ,;:") + "…"
}

func (sf *surface) mainProgGo() []byte {
	type mp struct{ attr, prog string }
	var all []mp
	for _, h := range sf.handles {
		if p := clean(h.info.MainProgram); p != "" && !strings.ContainsAny(p, "\t\n/") {
			all = append(all, mp{h.attr, p})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].attr < all[j].attr })
	var t strings.Builder
	t.WriteString("\n")
	for _, m := range all {
		t.WriteString(m.attr + "\t" + m.prog + "\n")
	}
	var b bytes.Buffer
	b.WriteString(header(sf.rev))
	b.WriteString("package nixext\n\n")
	b.WriteString("// mainPrograms holds meta.mainProgram of every handle that has one (\"\\n<attr>\\t<program>\" lines,\n// sorted by attribute), for Attr.Exe.\n")
	b.WriteString("const mainPrograms = " + strconv.Quote(t.String()) + "\n")
	return b.Bytes()
}

func (sf *surface) surfaceGo() []byte {
	var b bytes.Buffer
	b.WriteString(header(sf.rev))
	b.WriteString("package nixext\n\n")
	b.WriteString("// SurfaceRev is the nixpkgs revision the typed handles of package nixpkgs were generated from\n// (lock.DefaultRev of this bob).\n")
	b.WriteString("const SurfaceRev = " + strconv.Quote(sf.rev) + "\n")
	return b.Bytes()
}

// writeFiles writes the generated files.
func writeFiles(root string, files map[string][]byte) error {
	for _, f := range sortedKeys(files) {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(f)), files[f], 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// checkGenerated is -check (offline): the committed files equal a regeneration, the snapshot is
// of lock.DefaultRev, and every handle is in the snapshot and in the pinned index's names list.
func checkGenerated(root string) error {
	rev, err := defaultRev(root)
	if err != nil {
		return err
	}
	snapFile := snapshotPath(root, rev)
	if _, err := os.Stat(snapFile); err != nil {
		return fmt.Errorf("no snapshot for lock.DefaultRev %s (%w); run go run -tags online ./cmd/nixpkgsgen snapshot", rev, err)
	}
	s, err := readSnapshot(snapFile)
	if err != nil {
		return err
	}
	var problems []string
	if s.Rev != rev {
		problems = append(problems, fmt.Sprintf("snapshot revision %s differs from lock.DefaultRev %s", s.Rev, rev))
	}
	files, err := generate(root, snapFile)
	if err != nil {
		return err
	}
	for _, f := range sortedKeys(files) {
		have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		if !bytes.Equal(have, files[f]) {
			problems = append(problems, f+" differs from the generator's output (hand-edited or stale); run go generate ./pkg/nixpkgs")
		}
	}
	ex, _ := readExclude(root)
	inc, _ := readInclude(root)
	sf, err := selectSurface(s, ex, inc)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, n := range s.Names {
		names[n] = true
	}
	for _, h := range sf.handles {
		top, _, _ := strings.Cut(h.attr, ".")
		if _, ok := s.Attrs[h.attr]; !ok || !names[top] {
			problems = append(problems, fmt.Sprintf("handle %s (%s) is not in the pinned index's names list", h.name, h.attr))
		}
	}
	if len(problems) > 0 {
		return errors.New("check failed:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}
