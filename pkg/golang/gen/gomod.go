package gen

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

// localModule is another workspace module of this repository that a module imports: go.mod
// requires it at v0.0.0 and replaces it by its directory (the go command cannot find it
// otherwise); bob's build list does not contain it (R-GO-002b).
type localModule struct{ path, dir string }

// localVersion is the version a workspace module is required at (it is never fetched).
const localVersion = "v0.0.0"

// goModFiles returns go.mod and go.sum of module m (§03.2.5, R-GO-003): the two header lines,
// module, go X.Y.0, one block of direct requirements, one of indirect ones, both sorted;
// go.sum from bob.lock. The requirements are the roots resolution used (resolve.Roots): the
// module's declarations and every module of its build list that some workspace module
// declares (declared) or that provides packages, each at its selected version, so the go
// command selects exactly the locked build list. go.sum is left out while it would be empty.
func goModFiles(m *wsModule, sec *lock.GoSection, declared map[string]bool, local []localModule) []engine.GeneratedFile {
	direct := directRequires(m)
	var buildList []resolve.Module
	packages := map[string]bool{}
	if sec != nil {
		for _, mv := range sec.Workspaces[m.spec.Path].Modules {
			if p, v, ok := strings.Cut(mv, "@"); ok {
				buildList = append(buildList, resolve.Module{Path: p, Version: v})
			}
		}
		for _, e := range sec.Modules {
			if e.Packages {
				packages[e.Path] = true
			}
		}
	}
	sel := map[string]string{}
	for _, bm := range buildList {
		sel[bm.Path] = bm.Version
	}
	isDirect := map[string]bool{}
	var kept []gospec.Requirement
	for _, r := range direct {
		if v := sel[r.Path]; v != "" {
			r.Version = v // bob lock --update or a dependency moved it up
		}
		if strings.HasPrefix(r.Version, generatorPrefix) {
			continue // a generator runtime bob lock has not resolved yet
		}
		isDirect[r.Path] = true
		kept = append(kept, r)
	}
	direct = kept
	var indirect []gospec.Requirement
	for _, r := range resolve.Roots(buildList, declared, packages) {
		if !isDirect[r.Path] {
			indirect = append(indirect, gospec.Requirement{Path: r.Path, Version: r.Version})
		}
	}

	bobGo := "bob.go"
	if m.dir != "" {
		bobGo = m.dir + "/bob.go"
	}
	var b strings.Builder
	b.WriteString(engine.GoModHeader + "\n")
	fmt.Fprintf(&b, "// Edit %s and run bob lock.\n", bobGo)
	goLine := goDirectiveOf(m, buildList, sec)
	fmt.Fprintf(&b, "module %s\n\ngo %s\n", m.spec.Path, goLine)
	withLocal := direct
	if len(local) > 0 {
		withLocal = slices.Clone(direct)
		for _, l := range local {
			withLocal = append(withLocal, gospec.Requirement{Path: l.path, Version: localVersion})
		}
		sort.Slice(withLocal, func(i, j int) bool { return withLocal[i].Path < withLocal[j].Path })
	}
	if len(withLocal) > 0 {
		b.WriteString("\nrequire (\n")
		for _, r := range withLocal {
			fmt.Fprintf(&b, "\t%s %s\n", r.Path, r.Version)
		}
		b.WriteString(")\n")
	}
	if len(indirect) > 0 {
		b.WriteString("\nrequire (\n")
		for _, r := range indirect {
			fmt.Fprintf(&b, "\t%s %s // indirect\n", r.Path, r.Version)
		}
		b.WriteString(")\n")
	}
	for _, l := range local {
		fmt.Fprintf(&b, "\nreplace %s => %s\n", l.path, l.dir)
	}
	modPath := path.Join(m.dir, "go.mod")
	files := []engine.GeneratedFile{{Path: modPath, Content: []byte(b.String()), Mode: 0o444, Owner: m.node.Label,
		Committed: true, Explain: explainGoMod(goLine, withLocal, indirect, bobGo)}}

	if sum := goSum(buildList, sec); len(sum) > 0 {
		files = append(files, engine.GeneratedFile{Path: path.Join(m.dir, "go.sum"), Content: sum, Mode: 0o444,
			Owner: m.node.Label, Committed: true, Explain: explainGoSum(sum)})
	}
	return files
}

// goDirectiveOf is the go line of a module's go.mod: its declared Go ("1.26.0"), raised to the
// highest go line of a selected module of the same language version ("1.26.1"): the go
// command requires the main module's go line to cover its dependencies' (it would otherwise
// want to update go.mod). A newer language version is refused by bob lock (BOB-E-1008).
func goDirectiveOf(m *wsModule, buildList []resolve.Module, sec *lock.GoSection) string {
	out := goDirective(m.spec.Go)
	if sec == nil {
		return out
	}
	sel := map[string]string{}
	for _, bm := range buildList {
		sel[bm.Path] = bm.Version
	}
	for _, e := range sec.Modules {
		if sel[e.Path] == e.Version && e.Go != "" && resolve.GoLang(e.Go) == resolve.GoLang(out) && resolve.GoCompare(e.Go, out) > 0 {
			out = e.Go
		}
	}
	return out
}

// goDirective writes a go version with three parts ("1.26" → "1.26.0").
func goDirective(v string) string {
	if strings.Count(v, ".") == 1 {
		return v + ".0"
	}
	return v
}

// directRequires are the module's declared requirements plus the runtimes of its generated
// packages (§03.6.4), sorted by path; a declared version wins.
func directRequires(m *wsModule) []gospec.Requirement {
	byPath := map[string]string{}
	for _, g := range m.spec.Generated {
		for _, r := range g.Require {
			byPath[r.Path] = r.Version
		}
	}
	for _, r := range m.spec.Require {
		byPath[r.Path] = r.Version
	}
	out := make([]gospec.Requirement, 0, len(byPath))
	for _, p := range sortedKeys(byPath) {
		out = append(out, gospec.Requirement{Path: p, Version: byPath[p]})
	}
	return out
}

// goSum is go.sum for a build list: "<path> <version> <h1>" for the selected modules that
// provide packages, "<path> <version>/go.mod <modH1>" for every locked version of a module of
// the build list (the go command reads the go.mod of non-selected versions too, below
// unpruned modules), sorted bytewise.
func goSum(buildList []resolve.Module, sec *lock.GoSection) []byte {
	if sec == nil {
		return nil
	}
	sel := map[string]string{}
	for _, m := range buildList {
		sel[m.Path] = m.Version
	}
	var lines []string
	for _, m := range sec.Modules {
		v, ok := sel[m.Path]
		if !ok {
			continue
		}
		if m.Packages && m.H1 != "" && v == m.Version {
			lines = append(lines, fmt.Sprintf("%s %s %s", m.Path, m.Version, m.H1))
		}
		lines = append(lines, fmt.Sprintf("%s %s/go.mod %s", m.Path, m.Version, m.ModH1))
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n")
}

// modFile is what drift explanations read from a go.mod on disk.
type modFile struct {
	goVersion string
	require   map[string]string // path → version
}

// parseGoMod reads the go directive and the requirements of a go.mod (both require forms).
func parseGoMod(b []byte) modFile {
	m := modFile{require: map[string]string{}}
	sc := bufio.NewScanner(bytes.NewReader(b))
	inBlock := false
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case inBlock && f[0] == ")":
			inBlock = false
		case inBlock && len(f) >= 2:
			m.require[f[0]] = f[1]
		case f[0] == "go" && len(f) == 2:
			m.goVersion = f[1]
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			inBlock = true
		case f[0] == "require" && len(f) >= 3:
			m.require[f[1]] = f[2]
		}
	}
	return m
}

// explainGoMod maps a go.mod edited outside bob to its differences in bob.go terms (R-FW-061);
// no line means the file declares the same module (only layout or comments differ).
func explainGoMod(goWant string, direct, indirect []gospec.Requirement, bobGo string) func([]byte) []engine.DriftLine {
	want := map[string]string{}
	declared := map[string]bool{}
	for _, r := range direct {
		want[r.Path] = r.Version
		declared[r.Path] = true
	}
	for _, r := range indirect {
		want[r.Path] = r.Version
	}
	return func(onDisk []byte) []engine.DriftLine {
		got := parseGoMod(onDisk)
		var out []engine.DriftLine
		// "go 1.26" (go mod init, a hand-written go.mod) is the release bob writes as 1.26.0 (A452).
		if goDirective(got.goVersion) != goWant {
			out = append(out, engine.DriftLine{Op: "~", Text: fmt.Sprintf("go %s → %s (set Go in %s)", goWant, orNone(got.goVersion), bobGo)})
		}
		paths := map[string]bool{}
		for p := range want {
			paths[p] = true
		}
		for p := range got.require {
			paths[p] = true
		}
		for _, p := range sortedKeys(paths) {
			w, inWant := want[p]
			g, inGot := got.require[p]
			switch {
			case inWant && !inGot && len(got.require) == 0:
				// A skeleton (go mod init, or bob's file from before the requirements were
				// locked) lacks them all: nothing a user changed.
			case inWant && !inGot:
				out = append(out, engine.DriftLine{Op: "-", Text: fmt.Sprintf("%s %s  (%s declares it)", p, w, bobGo)})
			case !inWant && inGot:
				out = append(out, engine.DriftLine{Op: "+", Text: fmt.Sprintf("%s %s  (not declared in bob.go)", p, g)})
			case w != g && declared[p]:
				out = append(out, engine.DriftLine{Op: "~", Text: fmt.Sprintf("%s %s → %s  (%s declares %s)", p, w, g, bobGo, w)})
			case w != g:
				out = append(out, engine.DriftLine{Op: "~", Text: fmt.Sprintf("%s %s → %s  (bob.lock selects %s)", p, w, g, w)})
			}
		}
		return out // empty: the same module in bob.go terms (only layout or comments differ)
	}
}

// explainGoSum summarises a go.sum edited outside bob.
func explainGoSum(want []byte) func([]byte) []engine.DriftLine {
	return func(onDisk []byte) []engine.DriftLine {
		w, g := lineSet(want), lineSet(onDisk)
		var added, removed int
		for l := range g {
			if !w[l] {
				added++
			}
		}
		for l := range w {
			if !g[l] {
				removed++
			}
		}
		if added+removed == 0 {
			return nil // the same lines in another order or layout
		}
		return []engine.DriftLine{{Op: "~", Text: fmt.Sprintf("go.sum: %d lines added, %d removed (bob writes go.sum from bob.lock)", added, removed)}}
	}
}

func lineSet(b []byte) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out[l] = true
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
