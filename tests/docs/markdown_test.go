package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// docsDir is the documentation tree, relative to the repository root (the contract with the
// website renderer: website/content/docs/<NN-section>/<NN-page>.md with frontmatter).
const docsDir = "website/content/docs"

// heading is one ATX heading of a page.
type heading struct {
	level  int
	text   string
	anchor string
	line   int
}

// block is a fenced code block. Its info string is "<lang> [attr…]": the renderer shows only
// the language, the words after it are attributes for the tests (HARNESS.md §17):
//
//	```go file=cmd/hello/bob.go   a file of the page's example project (written in order)
//	```go verify                  a complete bob.go file, type-checked on its own
//	```bash run                   commands TestQuickstartsE2E runs in the project directory
//	```text expect                lines the preceding run block must print (… is a wildcard)
type block struct {
	lang    string            // info string's first word
	attrs   map[string]string // "file" → path; "verify", "run", "expect" → ""
	content string
	line    int // 1-based line of the opening fence
}

// link is a markdown link target outside code.
type link struct {
	target string
	line   int
}

// page is one parsed documentation page.
type page struct {
	rel      string // path below docsDir, slash separated
	abs      string
	front    map[string]string
	frontErr error
	lines    []string // the body lines (after the frontmatter), for prose checks
	bodyLine int      // line number of the first body line
	headings []heading
	blocks   []block
	links    []link
}

var (
	pageName    = regexp.MustCompile(`^[0-9]{2}-[a-z0-9]+(-[a-z0-9]+)*\.md$`)
	sectionName = regexp.MustCompile(`^[0-9]{2}-[a-z0-9]+(-[a-z0-9]+)*$`)
	linkRe      = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	codeSpanRe  = regexp.MustCompile("`[^`]*`")
)

// repoRoot is the repository root: the nearest ancestor of the working directory with
// go.work (the test runs in tests/docs).
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatalf("no go.work above %s", dir)
		}
	}
}

// loadPages parses every page of the documentation tree. Pages with "placeholder: true" in
// their frontmatter are the website unit's stand-ins (the renderer hides them once a real page
// exists) and are left out. Anything else that is not a page fails the test.
func loadPages(t testing.TB, root string) []*page {
	t.Helper()
	base := filepath.Join(root, docsDir)
	var pages []*page
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		p := parsePage(rel, path, string(b))
		if p.front["placeholder"] == "true" {
			return nil
		}
		dir, file, ok := strings.Cut(rel, "/")
		if !ok || strings.Contains(file, "/") || !sectionName.MatchString(dir) || !pageName.MatchString(file) {
			t.Errorf("%s: pages live at <NN-section>/<NN-page>.md (lower case, digits, -)", rel)
			return nil
		}
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", docsDir, err)
	}
	if len(pages) == 0 {
		t.Fatalf("%s holds no pages", docsDir)
	}
	return pages
}

// parsePage splits frontmatter and body and collects headings, blocks and links.
func parsePage(rel, abs, src string) *page {
	p := &page{rel: rel, abs: abs, front: map[string]string{}}
	all := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	start := 0
	if len(all) > 0 && all[0] == "---" {
		end := -1
		for i := 1; i < len(all); i++ {
			if all[i] == "---" {
				end = i
				break
			}
			k, v, ok := strings.Cut(all[i], ":")
			k = strings.TrimSpace(k)
			if !ok || k == "" {
				p.frontErr = fmt.Errorf("frontmatter line %d is not key: value", i+1)
				continue
			}
			if _, dup := p.front[k]; dup {
				p.frontErr = fmt.Errorf("frontmatter key %q appears twice", k)
			}
			p.front[k] = unquote(strings.TrimSpace(v))
		}
		if end < 0 {
			p.frontErr = fmt.Errorf("frontmatter is not closed with ---")
			end = len(all) - 1
		}
		start = end + 1
	} else {
		p.frontErr = fmt.Errorf("the page does not start with --- frontmatter")
	}
	p.lines = all[start:]
	p.bodyLine = start + 1

	seen := map[string]int{}
	for i := 0; i < len(p.lines); i++ {
		ln := p.lines[i]
		lineNo := p.bodyLine + i
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "```") {
			words := strings.Fields(strings.TrimPrefix(trim, "```"))
			b := block{attrs: map[string]string{}, line: lineNo}
			if len(words) > 0 {
				b.lang = words[0]
				for _, w := range words[1:] {
					k, v, _ := strings.Cut(w, "=")
					b.attrs[k] = v
				}
			}
			var body []string
			j := i + 1
			for ; j < len(p.lines); j++ {
				if strings.TrimSpace(p.lines[j]) == "```" {
					break
				}
				body = append(body, p.lines[j])
			}
			b.content = strings.Join(body, "\n") + "\n"
			p.blocks = append(p.blocks, b)
			i = j
			continue
		}
		if lvl, text, ok := atxHeading(ln); ok {
			a := slug(text)
			if n, dup := seen[a]; dup {
				seen[a] = n + 1
				a = a + "-" + strconv.Itoa(n+1)
			} else {
				seen[a] = 0
			}
			p.headings = append(p.headings, heading{level: lvl, text: text, anchor: a, line: lineNo})
		}
		for _, m := range linkRe.FindAllStringSubmatch(codeSpanRe.ReplaceAllString(ln, ""), -1) {
			p.links = append(p.links, link{target: m[1], line: lineNo})
		}
	}
	return p
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// atxHeading parses "## Text" (up to six #, a space, no closing #s needed).
func atxHeading(ln string) (int, string, bool) {
	n := 0
	for n < len(ln) && ln[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(ln) || ln[n] != ' ' {
		return 0, "", false
	}
	return n, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(ln[n:]), "#")), true
}

// slug is the anchor of a heading as the website renders it (website/src/lib/server/
// markdown.ts slugify, applied to the heading's plain text): links replaced by their text,
// lower-cased, backticks and *_~[]() removed, every other character that is not a letter,
// digit, white space or '-' removed, trimmed, white space runs replaced by '-'; "section" when
// nothing is left. Duplicates get -1, -2, … (parsePage).
func slug(text string) string {
	text = mdLink.ReplaceAllString(text, "$1")
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case strings.ContainsRune("`*_~[]()", r):
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || unicode.IsSpace(r):
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), "-")
	if s == "" {
		return "section"
	}
	return s
}

var mdLink = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)

// prose is the page's text outside fenced code blocks (what a reader sees as written
// documentation). Inline code spans are kept.
func (p *page) prose() string {
	var out []string
	inFence := false
	for _, ln := range p.lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// text is the whole body (code blocks included).
func (p *page) text() string { return strings.Join(p.lines, "\n") }

// section returns the body lines from the heading that matches pred to the next heading of
// the same or a higher level.
func (p *page) section(pred func(heading) bool) (heading, []string, bool) {
	for i, h := range p.headings {
		if !pred(h) {
			continue
		}
		end := len(p.lines)
		for _, h2 := range p.headings[i+1:] {
			if h2.level <= h.level {
				end = h2.line - p.bodyLine
				break
			}
		}
		return h, p.lines[h.line-p.bodyLine+1 : end], true
	}
	return heading{}, nil, false
}

// findPage returns the page with the given path below docsDir.
func findPage(t testing.TB, pages []*page, rel string) *page {
	t.Helper()
	for _, p := range pages {
		if p.rel == rel {
			return p
		}
	}
	t.Fatalf("documentation page %s/%s is missing", docsDir, rel)
	return nil
}

// checkGenerated compares the body of a generated page (everything after the frontmatter)
// with want, or rewrites it with BOB_DOCS_UPDATE=1. Generated pages have no hand-written body:
// the website renders raw HTML as text, so there is no invisible marker to fence a generated
// region with.
func checkGenerated(t *testing.T, p *page, want string) {
	t.Helper()
	got := strings.Join(p.lines, "\n")
	if got == want {
		return
	}
	if os.Getenv("BOB_DOCS_UPDATE") == "1" {
		src, err := os.ReadFile(p.abs)
		if err != nil {
			t.Fatal(err)
		}
		all := strings.Split(string(src), "\n")
		head := strings.Join(all[:p.bodyLine-1], "\n") + "\n"
		if err := os.WriteFile(p.abs, []byte(head+want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: rewrote the generated page", p.rel)
		return
	}
	t.Errorf("%s: the generated page is out of date; run BOB_DOCS_UPDATE=1 go test ./tests/docs and review the diff\n%s",
		p.rel, firstDiff(got, want))
}

// firstDiff describes the first differing line of two texts.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < max(len(g), len(w)); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return fmt.Sprintf("first difference at line %d:\n  page: %q\n  code: %q", i+1, a, b)
		}
	}
	return ""
}
