package docs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Pages with a fixed role (paths below docsDir).
const (
	cliPage      = "05-cli/01-commands.md"
	errorsPage   = "05-cli/02-errors.md"
	apiIndexPage = "07-reference/02-api-index.md"
	configPage   = "06-server/03-configuration.md"
)

// frontKeys are the frontmatter keys of the renderer contract.
var frontKeys = []string{"order", "section", "summary", "title"}

func TestFrontmatter(t *testing.T) {
	pages := loadPages(t, repoRoot(t))
	sectionOf := map[string]string{} // directory → section title
	dirOf := map[string]string{}     // section title → directory
	titles := map[string]string{}
	for _, p := range pages {
		if p.frontErr != nil {
			t.Errorf("%s: %v", p.rel, p.frontErr)
			continue
		}
		var keys []string
		for k := range p.front {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, frontKeys) {
			t.Errorf("%s: frontmatter keys are %v, want exactly %v", p.rel, keys, frontKeys)
		}
		for _, k := range frontKeys {
			if v := p.front[k]; strings.TrimSpace(v) == "" {
				t.Errorf("%s: frontmatter %s is empty", p.rel, k)
			}
		}
		dir, file := path.Split(p.rel)
		dir = strings.TrimSuffix(dir, "/")
		nn, _ := strconv.Atoi(file[:2])
		if o, err := strconv.Atoi(p.front["order"]); err != nil || o != nn {
			t.Errorf("%s: order is %q, want %d (the file's NN prefix)", p.rel, p.front["order"], nn)
		}
		sec := p.front["section"]
		if s, ok := sectionOf[dir]; ok && s != sec {
			t.Errorf("%s: section %q, but other pages of %s say %q", p.rel, sec, dir, s)
		}
		sectionOf[dir] = sec
		if d, ok := dirOf[sec]; ok && d != dir {
			t.Errorf("%s: section %q is also used by %s", p.rel, sec, d)
		}
		dirOf[sec] = dir
		if len(p.front["title"]) > 80 {
			t.Errorf("%s: title longer than 80 characters", p.rel)
		}
		if len(p.front["summary"]) > 200 {
			t.Errorf("%s: summary longer than 200 characters", p.rel)
		}
		if other, dup := titles[p.front["title"]]; dup {
			t.Errorf("%s: title %q is also used by %s", p.rel, p.front["title"], other)
		}
		titles[p.front["title"]] = p.rel
		if len(p.headings) > 0 && p.headings[0].level == 1 {
			t.Errorf("%s:%d: the renderer prints the title; pages start at ## headings", p.rel, p.headings[0].line)
		}
		last := ""
		for _, h := range p.headings {
			if h.level == 2 {
				last = h.text
			}
		}
		if last != "Next steps" {
			t.Errorf("%s: the last ## heading is %q; every page ends with ## Next steps", p.rel, last)
		}
	}
}

func TestLinks(t *testing.T) {
	root := repoRoot(t)
	pages := loadPages(t, root)
	anchors := map[string]map[string]bool{}
	for _, p := range pages {
		a := map[string]bool{}
		for _, h := range p.headings {
			a[h.anchor] = true
		}
		anchors[p.rel] = a
	}
	for _, p := range pages {
		for _, l := range p.links {
			tg := l.target
			switch {
			case strings.HasPrefix(tg, "https://"), strings.HasPrefix(tg, "http://"), strings.HasPrefix(tg, "mailto:"):
				continue
			case strings.HasPrefix(tg, "/"):
				t.Errorf("%s:%d: link %q is absolute; link pages relatively (../section/NN-page.md)", p.rel, l.line, tg)
				continue
			}
			file, frag, _ := strings.Cut(tg, "#")
			target := p.rel
			if file != "" {
				if !strings.HasSuffix(file, ".md") {
					t.Errorf("%s:%d: link %q is not a documentation page; name repository files as code (`SPEC.md`), not as links", p.rel, l.line, tg)
					continue
				}
				target = path.Clean(path.Join(path.Dir(p.rel), file))
			}
			a, ok := anchors[target]
			if !ok {
				t.Errorf("%s:%d: link %q points to %s, which is not a page", p.rel, l.line, tg, target)
				continue
			}
			if frag != "" && !a[frag] {
				t.Errorf("%s:%d: link %q: %s has no heading with anchor #%s", p.rel, l.line, tg, target, frag)
			}
		}
	}
}

// flagCell matches the first cell of a flags table row: | `--name …` | or | `-x …` |.
var flagCell = regexp.MustCompile("^\\|\\s*`(-{1,2})([a-zA-Z][a-zA-Z0-9-]*)")

// tableFlags returns the flags named in the first cell of the table rows of lines.
func tableFlags(lines []string) []string {
	var out []string
	for _, ln := range lines {
		if m := flagCell.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			out = append(out, m[2])
		}
	}
	return out
}

func TestCLIReference(t *testing.T) {
	root := repoRoot(t)
	p := findPage(t, loadPages(t, root), cliPage)
	cmds, global := cliSurface(t, root)

	_, lines, ok := p.section(func(h heading) bool { return h.level == 2 && h.text == "Global flags" })
	if !ok {
		t.Fatalf("%s: no ## Global flags section", cliPage)
	}
	documented := tableFlags(lines)
	for _, f := range global {
		if !slices.Contains(documented, f) {
			t.Errorf("%s: global flag %s (pkg/cli/args.go) is not in the ## Global flags table", cliPage, spell(f))
		}
	}
	for _, f := range documented {
		if !slices.Contains(global, f) {
			t.Errorf("%s: ## Global flags documents %s, which bob does not have", cliPage, spell(f))
		}
	}

	known := map[string]bool{}
	for _, c := range cmds {
		known[c.name] = true
		h, lines, ok := p.section(func(h heading) bool { return h.level == 3 && commandOf(h.text) == c.name })
		if !ok {
			t.Errorf("%s: command %q (pkg/cli/cli.go) has no ### bob %s section", cliPage, c.name, c.name)
			continue
		}
		documented := tableFlags(lines)
		for _, f := range c.flags {
			if !slices.Contains(documented, f) {
				t.Errorf("%s:%d: flag %s of bob %s is not in the section's flags table", cliPage, h.line, spell(f), c.name)
			}
		}
		for _, f := range documented {
			if !slices.Contains(c.flags, f) && !slices.Contains(global, f) {
				t.Errorf("%s:%d: bob %s documents %s, which the command does not have", cliPage, h.line, c.name, spell(f))
			}
		}
	}
	for _, h := range p.headings {
		if h.level == 3 && strings.HasPrefix(h.text, "bob ") && !known[commandOf(h.text)] {
			t.Errorf("%s:%d: ### %s documents a command bob does not have", cliPage, h.line, h.text)
		}
	}

	// Exit codes: one row per status bob can return (diag.ExitCode over every class, and 0).
	_, lines, ok = p.section(func(h heading) bool { return h.level == 2 && h.text == "Exit codes" })
	if !ok {
		t.Fatalf("%s: no ## Exit codes section", cliPage)
	}
	want := map[int]bool{0: true}
	for c := diag.Class(0); c <= diag.ClassInternal; c++ {
		want[diag.ExitCode([]*diag.Error{{Code: diag.Code(int(c) * 1000), Severity: diag.SeverityError}})] = true
	}
	rows := strings.Join(lines, "\n")
	for code := range want {
		if !regexp.MustCompile(`(?m)^\|\s*` + strconv.Itoa(code) + `\s*\|`).MatchString(rows) {
			t.Errorf("%s: ## Exit codes has no row for exit status %d", cliPage, code)
		}
	}
}

// commandOf returns the command word of a "bob <cmd> …" heading, with backticks removed.
func commandOf(text string) string {
	f := strings.Fields(strings.ReplaceAll(text, "`", ""))
	if len(f) < 2 || f[0] != "bob" {
		return ""
	}
	return f[1]
}

func spell(f string) string {
	if len(f) <= 2 { // -C, -v, -vv, -q, -j, -f
		return "-" + f
	}
	return "--" + f
}

func TestErrorTable(t *testing.T) {
	root := repoRoot(t)
	p := findPage(t, loadPages(t, root), errorsPage)
	var b strings.Builder
	b.WriteString(errorsIntro)
	b.WriteString("| Code | Class | Exit | Message | Hint |\n|---|---|---|---|---|\n")
	for _, c := range diag.Codes() {
		e, _ := diag.Lookup(c)
		exit := diag.ExitCode([]*diag.Error{{Code: c, Severity: diag.SeverityError}})
		for _, v := range e.Variants {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", c.ID(), c.Class(), exit, cell(v.Message), cell(strings.Join(v.Hints, " / ")))
		}
	}
	b.WriteString(errorsOutro)
	checkGenerated(t, p, b.String())
}

// cell escapes a table cell: inline code keeps template braces readable, pipes are escaped.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return "`" + strings.ReplaceAll(s, "|", `\|`) + "`"
}

func TestAPIReference(t *testing.T) {
	root := repoRoot(t)
	pages := loadPages(t, root)
	idx := findPage(t, pages, apiIndexPage)

	// Every package below pkg/output and pkg/services is a public API package.
	for _, parent := range []string{"pkg/output", "pkg/services"} {
		ents, err := os.ReadDir(filepath.Join(root, parent))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if e.IsDir() && !slices.Contains(apiPackages, parent+"/"+e.Name()) {
				t.Errorf("%s/%s is a public API package missing from apiPackages (tests/docs/surface_test.go)", parent, e.Name())
			}
		}
	}

	var prose strings.Builder
	for _, p := range pages {
		if p.rel != apiIndexPage {
			prose.WriteString(p.prose())
			prose.WriteByte('\n')
		}
	}
	all := prose.String()

	var gen strings.Builder
	gen.WriteString(apiIntro)
	for _, dir := range apiPackages {
		name, funcs, types := apiSurface(t, root, dir)
		fmt.Fprintf(&gen, "## %s\n\n`import \"github.com/megakuul/bob/%s\"`\n\n", strings.TrimPrefix(dir, "pkg/"), dir)
		gen.WriteString("Functions:\n\n")
		n := 0
		for _, f := range funcs {
			if f.recv != "" {
				continue
			}
			n++
			fmt.Fprintf(&gen, "- `%s`\n", f.sig)
			if !strings.Contains(all, name+"."+f.name) {
				t.Errorf("%s.%s (%s) is not mentioned in the documentation (write `%s.%s` in the bob.go reference, 04-guides)", name, f.name, dir, name, f.name)
			}
		}
		if n == 0 {
			gen.WriteString("- none\n")
		}
		gen.WriteString("\nTypes and methods:\n\n")
		for _, ty := range types {
			fmt.Fprintf(&gen, "- `%s.%s`", name, ty.name)
			if ty.doc != "" {
				fmt.Fprintf(&gen, ": %s", strings.ReplaceAll(ty.doc, "|", `\|`))
			}
			gen.WriteByte('\n')
			for _, f := range funcs {
				if f.recv == ty.name {
					fmt.Fprintf(&gen, "  - `%s`\n", f.sig)
				}
			}
		}
		gen.WriteByte('\n')
	}
	gen.WriteString(apiOutro)
	checkGenerated(t, idx, gen.String())
}

// envName matches a BOB_* variable name in documentation text.
var envName = regexp.MustCompile(`\bBOB_[A-Z0-9_]+`)

func TestEnvironmentVariables(t *testing.T) {
	root := repoRoot(t)
	pages := loadPages(t, root)
	cfg := findPage(t, pages, configPage)

	server := envVars(t, root, "internal", "cmd/bob-server")
	client := envVars(t, root, "pkg", "cmd/bob")
	if len(server) < 10 || len(client) < 5 {
		t.Fatalf("found %d server and %d client BOB_* variables; the scan is broken", len(server), len(client))
	}

	var cfgRows []string
	for _, ln := range cfg.lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "|") {
			cfgRows = append(cfgRows, ln)
		}
	}
	rows := strings.Join(cfgRows, "\n")
	for v, file := range server {
		if !strings.Contains(rows, "`"+v+"`") {
			t.Errorf("%s: bob-server reads %s (%s), but no table row of the page names `%s`", configPage, v, file, v)
		}
	}
	var docs strings.Builder
	for _, p := range pages {
		docs.WriteString(p.text())
	}
	text := docs.String()
	for v, file := range client {
		if !strings.Contains(text, v) {
			t.Errorf("bob reads %s (%s), but no documentation page mentions it", v, file)
		}
	}
	for _, p := range pages {
		for i, ln := range p.lines {
			for _, v := range envName.FindAllString(ln, -1) {
				if _, ok := server[v]; ok {
					continue
				}
				if _, ok := client[v]; ok {
					continue
				}
				t.Errorf("%s:%d: %s is not read by bob or bob-server (stale or misspelled)", p.rel, p.bodyLine+i, v)
			}
		}
	}
}

// TestServerModes: every mode in bob-server's usage text is documented.
func TestServerModes(t *testing.T) {
	root := repoRoot(t)
	pages := loadPages(t, root)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "cmd/bob-server/main.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	usage := ""
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if ok && len(vs.Names) == 1 && vs.Names[0].Name == "usage" && len(vs.Values) == 1 {
			usage = stringLit(t, vs.Values[0])
		}
		return true
	})
	_, after, ok := strings.Cut(usage, "modes:\n")
	if !ok {
		t.Fatal("cmd/bob-server/main.go: the usage text has no modes: list")
	}
	var text strings.Builder
	for _, p := range pages {
		if strings.HasPrefix(p.rel, "06-server/") {
			text.WriteString(p.text())
		}
	}
	modes := 0
	for _, ln := range strings.Split(after, "\n") {
		if !strings.HasPrefix(ln, "  ") {
			break
		}
		words := strings.Fields(ln)
		mode := words[0]
		if len(words) > 1 && !strings.HasPrefix(words[1], "check") && words[0] == "store" {
			mode += " " + words[1]
		}
		modes++
		if !strings.Contains(text.String(), "bob-server "+mode) {
			t.Errorf("bob-server mode %q is not documented in 06-server", mode)
		}
	}
	if modes < 3 {
		t.Fatalf("read %d bob-server modes; the usage text changed shape", modes)
	}
}

// TestSnippetsTypeCheck type-checks the bob.go files of every page's example project
// (<!-- file: …/bob.go --> blocks, with the page's go.mod) and every standalone
// <!-- verify --> go block against this checkout's framework.
func TestSnippetsTypeCheck(t *testing.T) {
	root := repoRoot(t)
	pages := loadPages(t, root)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go binary on PATH")
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	vet := func(t *testing.T, dir, module string, files map[string]string) {
		t.Helper()
		mod := fmt.Sprintf("module %s\n\ngo 1.26.0\n\nrequire github.com/megakuul/bob v0.0.0-00010101000000-000000000000\n\nreplace github.com/megakuul/bob => %s\n", module, root)
		files["go.mod"], files["go.sum"] = mod, string(sum)
		for name, content := range files {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(goBin, "vet", "-tags=bob", "./...")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go vet -tags=bob: %v\n%s", err, out)
		}
	}

	snippets := map[string]string{}
	for _, p := range pages {
		files := map[string]string{}
		for _, b := range p.blocks {
			if name, ok := b.attrs["file"]; ok {
				files[name] = b.content
			}
			if _, ok := b.attrs["verify"]; ok {
				if b.lang != "go" {
					t.Errorf("%s:%d: verify marks a %q block; only go blocks are type-checked", p.rel, b.line, b.lang)
					continue
				}
				dir := fmt.Sprintf("s%03d", len(snippets))
				snippets[dir+"/bob.go"] = b.content
				t.Logf("%s:%d → %s", p.rel, b.line, dir)
			}
		}
		bobFiles := map[string]string{}
		for name, content := range files {
			if path.Base(name) == "bob.go" {
				bobFiles[name] = content
			}
		}
		if len(bobFiles) == 0 {
			continue
		}
		module := modulePath(files["go.mod"])
		if module == "" {
			t.Errorf("%s: the page's project has bob.go files but no file=go.mod block with a module line", p.rel)
			continue
		}
		t.Run(p.rel, func(t *testing.T) {
			vet(t, t.TempDir(), module, bobFiles)
		})
	}
	if len(snippets) > 0 {
		t.Run("verify-blocks", func(t *testing.T) { vet(t, t.TempDir(), "example.com/snippets", snippets) })
	}
}

func modulePath(gomod string) string {
	for _, ln := range strings.Split(gomod, "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(ln), "module "); ok {
			return strings.TrimSpace(m)
		}
	}
	return ""
}

// The fixed text of the generated pages (their whole body is generated).
const (
	errorsIntro = `Every error bob prints has a stable code, ` + "`BOB-E-NNNN`" + `. The first digit is the class, and
the class decides the exit status (see [Exit codes](01-commands.md#exit-codes)). Codes are never
reused or renumbered. In the messages, ` + "`{name}`" + ` is filled in with the details of the error.

> [!NOTE]
> This page is generated from bob's code table (` + "`pkg/bob/diag`" + `), so it always matches the
> binary built from the same source. Several hints are separated by " / "; bob prints the first
> one whose details it knows.

## Code table

`
	errorsOutro = `
## Next steps

- [CLI reference](01-commands.md): every command and flag.
- [Troubleshooting](../06-server/13-troubleshooting.md): server-side problems.
- [Limitations and status](../07-reference/03-status.md): what is not finished yet.
`
	apiIntro = `This page lists every exported function, type and method of the packages a ` + "`bob.go`" + ` file
may import. It is generated from the source code, so it always matches the framework of the
same bob version. For explanations and examples, read the
[bob.go reference](../04-guides/01-bob-go-reference.md).

> [!NOTE]
> Types are listed with the first sentence of their documentation. Struct fields and option
> details are in the doc comments: run ` + "`go doc github.com/megakuul/bob/pkg/golang`" + ` (or
> any other package) in a checkout of bob, or hover in your editor.

`
	apiOutro = `## Next steps

- [bob.go reference](../04-guides/01-bob-go-reference.md): how to use these packages.
- [Go quickstart](../03-quickstart/01-go.md): a complete project.
`
)
