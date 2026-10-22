package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// runEditor is `bob editor vscode|nvim|helix|zed|jetbrains [--write]` (SPEC §02.7.6,
// R-DEV-011): it prints the settings for the ecosystems the project uses and, with --write,
// merges them into the project's settings file.
func runEditor(e *env) int {
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob editor", "what", "an editor ("+strings.Join(devenv.Editors, ", ")+")"))
	}
	name := e.a.pos[0]
	if _, err := devenv.EditorSettings(name, nil); err != nil {
		return e.out.fail(err)
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	g, err := build.Evaluate(e.ctx, build.Options{Root: root})
	if err != nil {
		return e.out.fail(err)
	}
	used := map[string]bool{}
	for _, n := range g.Nodes {
		if eco, _, ok := strings.Cut(n.Kind, "."); ok && eco != "bob" && eco != "nixpkgs" {
			used[eco] = true
		}
	}
	var ecos []string
	for eco := range used {
		ecos = append(ecos, eco)
	}
	sort.Strings(ecos)
	s, err := devenv.EditorSettings(name, ecos)
	if err != nil {
		return e.out.fail(err)
	}
	changed := false
	if e.a.bool("write") && s.File != "" {
		changed, err = s.Merge(root)
		if err != nil {
			e.out.fail(err)
			// The snippet to paste by hand (BOB-E-0004 names the file).
			fmt.Fprintf(e.out.errw, "\nadd to %s:\n%s", s.File, s.Snippet())
			return diag.ExitCode(e.out.errs)
		}
	}
	if e.out.json {
		e.out.result(struct {
			Editor   string         `json:"editor"`
			File     string         `json:"file,omitempty"`
			Settings map[string]any `json:"settings,omitempty"`
			Written  bool           `json:"written"`
			Note     string         `json:"note,omitempty"`
		}{name, s.File, s.Values, changed, s.Note})
		return e.out.finish(0, translate.Counts{}, nil)
	}
	switch {
	case e.a.bool("write") && s.File != "":
		verb := "unchanged"
		if changed {
			verb = "updated"
		}
		e.out.printf("%s %s\n", s.File, verb)
	case s.File != "":
		e.out.printf("%s:\n%s", s.File, s.Snippet())
	}
	if s.Note != "" {
		e.out.printf("%s\n", s.Note)
	}
	if s.File != "" {
		if _, err := os.Stat(filepath.Join(root, ".bob", "bin")); err != nil {
			e.out.printf("the settings use the shims in .bob/bin: run bob env once (or cd into the project with the shell hook)\n")
		}
	}
	return 0
}
