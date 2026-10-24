package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func init() {
	register(Stage{Name: "docs", Order: 9, Run: runDocs})
}

// runDocs is the documentation guardrail (HARNESS.md §17): the tests of tests/docs (CLI,
// API, error codes, server variables, snippets, links, frontmatter against the code) and the
// change-coupling rule: a change of a public surface must change website/content/docs or
// carry a "docs-exempt: <reason>" line.
func runDocs(ctx context.Context, env *Env) error {
	if out, err := goCmd(ctx, env.Root, nil, "test", "-count=1", "./tests/docs"); err != nil {
		return fmt.Errorf("docs: go test ./tests/docs: %w\n%s", err, out)
	}
	ch, notice, err := gitChange(ctx, env.Root)
	if err != nil {
		return err
	}
	if notice != "" {
		fmt.Println("docs: " + notice)
		return nil
	}
	fmt.Printf("docs: change against %s: %d files\n", ch.base, len(ch.files))
	return ch.check()
}

// docsChange is what the coupling rule looks at: the files changed against the base, the
// changed lines that name a BOB_* variable in server code, and the exemptions found.
type docsChange struct {
	base     string
	files    []string // slash paths relative to the repository root
	envFiles []string // server Go files whose changed lines add or remove a "BOB_… literal
	exempt   string   // the reason of the first "docs-exempt:" line, "" for none
}

// docsTree is the documentation the rule asks for.
const docsTree = "website/content/docs/"

// apiDirs are the public bob.go API packages (the same list as apiPackages in
// tests/docs/surface_test.go; the tools module cannot import that test package).
var apiDirs = []string{
	"pkg/bob", "pkg/nixpkgs",
	"pkg/golang", "pkg/cc", "pkg/rust", "pkg/npm", "pkg/ocaml",
	"pkg/proto", "pkg/connect", "pkg/graphql", "pkg/openapi",
	"pkg/services/postgres",
	"pkg/output/oci", "pkg/output/tarball", "pkg/output/appimage", "pkg/release",
}

// surfaceFiles are single files of the public surface.
var surfaceFiles = []string{
	"pkg/cli/cli.go",         // the command table
	"pkg/cli/args.go",        // the global flags
	"pkg/bob/diag/codes.go",  // the error code table
	"cmd/bob-server/main.go", // the server modes
	"SPEC.md",
}

// isSurface reports whether a changed file is part of the documented public surface
// (server variables are judged by their changed lines, see envFiles).
func isSurface(p string) bool {
	if slices.Contains(surfaceFiles, p) {
		return true
	}
	if strings.HasPrefix(p, "spec/") && strings.HasSuffix(p, ".md") {
		return true
	}
	if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
		return false
	}
	return slices.Contains(apiDirs, path.Dir(p))
}

// check applies the rule.
func (c docsChange) check() error {
	var surface []string
	docs := false
	for _, f := range c.files {
		if strings.HasPrefix(f, docsTree) {
			docs = true
		}
		if isSurface(f) {
			surface = append(surface, f)
		}
	}
	for _, f := range c.envFiles {
		if !slices.Contains(surface, f) {
			surface = append(surface, f+" (BOB_* variable)")
		}
	}
	if len(surface) == 0 || docs {
		return nil
	}
	if c.exempt != "" {
		fmt.Printf("docs: public surface changed without docs, exempt: %s\n", c.exempt)
		return nil
	}
	slices.Sort(surface)
	return fmt.Errorf("docs: the change touches the public surface but not %s:\n  %s\n"+
		"update the documentation in the same change (AGENTS.md §5.9), or add a line "+
		"\"docs-exempt: <reason>\" to the commit message or to plan/decisions.md / plan/progress/<unit>.md",
		docsTree, strings.Join(surface, "\n  "))
}

// exemptRe matches an exemption line with a reason.
var exemptRe = regexp.MustCompile(`(?m)docs-exempt:[ \t]*(\S.*)$`)

func exemption(text string) string {
	if m := exemptRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// envLineRe matches a changed diff line that names a BOB_* string literal.
var envLineRe = regexp.MustCompile(`^[+-][^+-].*"BOB_[A-Z0-9_]+`)

// envChangedFiles returns the non-test Go files of a unified diff (-U0) whose changed lines
// add or remove a "BOB_… literal.
func envChangedFiles(diff string) []string {
	var out []string
	file := ""
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		ln := sc.Text()
		if f, ok := strings.CutPrefix(ln, "+++ b/"); ok {
			file = f
			continue
		}
		if strings.HasPrefix(ln, "+++ ") || strings.HasPrefix(ln, "--- ") {
			continue
		}
		if file == "" || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		if envLineRe.MatchString(ln) && !slices.Contains(out, file) {
			out = append(out, file)
		}
	}
	return out
}

// errNoGit marks a tree without usable git history (the rule is skipped with a notice).
var errNoGit = errors.New("no git")

// git runs a read-only git command in root. GIT_OPTIONAL_LOCKS=0 keeps git from refreshing
// the index, so the stage never writes to the repository.
func git(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// docsBase picks the commit to compare with: $BOB_DOCS_BASE, else the merge base of HEAD and
// the newest docs-verified* tag, origin's default branch, main or master (in that order).
// A merge base equal to HEAD (on the default branch itself) falls back to HEAD when the work
// tree has uncommitted changes, else to HEAD~1.
func docsBase(ctx context.Context, root string) (string, error) {
	if b := os.Getenv("BOB_DOCS_BASE"); b != "" {
		if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", b+"^{commit}"); err != nil {
			return "", fmt.Errorf("docs: BOB_DOCS_BASE=%s is not a commit", b)
		}
		return b, nil
	}
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", errNoGit
	}
	var refs []string
	if t, err := git(ctx, root, "describe", "--tags", "--abbrev=0", "--match", "docs-verified*", "HEAD"); err == nil && t != "" {
		refs = append(refs, t)
	}
	if o, err := git(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && o != "" {
		refs = append(refs, o)
	}
	refs = append(refs, "main", "master")
	for _, r := range refs {
		if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", r+"^{commit}"); err != nil {
			continue
		}
		mb, err := git(ctx, root, "merge-base", "HEAD", r)
		if err != nil || mb == "" {
			continue
		}
		if mb != head {
			return mb, nil
		}
		break
	}
	// On the default branch itself (or a branch without commits of its own): uncommitted
	// work is the change; a clean tree is checked against its last commit.
	if st, err := git(ctx, root, "status", "--porcelain"); err == nil && st != "" {
		return head, nil
	}
	if p, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD~1"); err == nil && p != "" {
		return p, nil
	}
	return "", errNoGit
}

// gitChange collects the change against the base: committed (base..HEAD), uncommitted and
// untracked files. Without git or history it returns a notice instead.
func gitChange(ctx context.Context, root string) (docsChange, string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return docsChange{}, "git is not installed; the change-coupling rule is skipped", nil
	}
	if _, err := git(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return docsChange{}, "not a git work tree; the change-coupling rule is skipped", nil
	}
	base, err := docsBase(ctx, root)
	if errors.Is(err, errNoGit) {
		return docsChange{}, "no base commit (empty history); the change-coupling rule is skipped", nil
	} else if err != nil {
		return docsChange{}, "", err
	}
	c := docsChange{base: base}
	add := func(out string) {
		for _, f := range strings.Split(out, "\n") {
			if f = strings.TrimSpace(f); f != "" && !slices.Contains(c.files, f) {
				c.files = append(c.files, f)
			}
		}
	}
	// git diff <base> compares the base with the work tree: committed and uncommitted changes.
	out, err := git(ctx, root, "diff", "--name-only", base)
	if err != nil {
		return c, "", harnessErr(fmt.Errorf("git diff --name-only %s: %w", base, err))
	}
	add(out)
	untracked, err := git(ctx, root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return c, "", harnessErr(fmt.Errorf("git ls-files: %w", err))
	}
	add(untracked)

	diff, err := git(ctx, root, "diff", "-U0", base, "--", "internal", "cmd/bob-server")
	if err != nil {
		return c, "", harnessErr(fmt.Errorf("git diff %s: %w", base, err))
	}
	c.envFiles = envChangedFiles(diff)
	for _, f := range strings.Split(untracked, "\n") {
		if (strings.HasPrefix(f, "internal/") || strings.HasPrefix(f, "cmd/bob-server/")) &&
			strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
			if b, err := os.ReadFile(filepath.Join(root, f)); err == nil && strings.Contains(string(b), `"BOB_`) {
				c.envFiles = append(c.envFiles, f)
			}
		}
	}

	// Exemptions: commit messages since the base, and added lines of the decision log and the
	// progress files (committed, uncommitted or untracked).
	msgs, _ := git(ctx, root, "log", "--format=%B", base+"..HEAD")
	c.exempt = exemption(msgs)
	if c.exempt == "" {
		d, _ := git(ctx, root, "diff", "-U0", base, "--", "plan/decisions.md", "plan/progress")
		var added []string
		for _, ln := range strings.Split(d, "\n") {
			if strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++") {
				added = append(added, ln[1:])
			}
		}
		c.exempt = exemption(strings.Join(added, "\n"))
	}
	if c.exempt == "" {
		for _, f := range strings.Split(untracked, "\n") {
			if strings.HasPrefix(f, "plan/progress/") {
				if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
					if c.exempt = exemption(string(b)); c.exempt != "" {
						break
					}
				}
			}
		}
	}
	return c, "", nil
}
