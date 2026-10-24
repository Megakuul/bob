package cli

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/parity"
)

// command is one row of the command table of SPEC §02.1 (R-CLI-001). Synopsis is the full
// synopsis of the table; it is empty for the commands whose synopsis the table leaves to §02.8
// ("see §02.8"), where only the `usage: bob <cmd>` prefix is checked.
type command struct {
	Args     []string // words that select the command: "pipeline run" is {"pipeline"}
	Synopsis []string
}

// commandTable is the command list of the harness, copied from SPEC §02.1.
func commandTable() []command {
	return []command{
		{[]string{"build"}, []string{"bob build [patterns…] [--remote] [--platform P] [--variant V] [--print-paths] [--keep-going=true] [--lock]"}},
		{[]string{"test"}, []string{"bob test [patterns…] [--remote] [--rerun] [--lock] [--require-all-tests] [-- test args]"}},
		{[]string{"run"}, []string{"bob run <label> [--platform native] [-- args]"}},
		{[]string{"dev"}, []string{"bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]"}},
		{[]string{"env"}, []string{"bob env [--shell <name>] [--format sh|fish|json] [--hook=bash|zsh|fish]"}},
		{[]string{"hook"}, []string{"bob hook bash|zsh|fish"}},
		{[]string{"allow"}, []string{"bob allow [dir]"}},
		{[]string{"deny"}, []string{"bob deny [dir]"}},
		{[]string{"exec"}, []string{"bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]"}},
		{[]string{"editor"}, []string{"bob editor vscode|nvim|helix|zed|jetbrains [--write]"}},
		{[]string{"check"}, []string{"bob check [--fix]"}},
		{[]string{"gen"}, []string{"bob gen [patterns…] [--check] [--force]"}},
		{[]string{"status"}, []string{"bob status [--json]"}},
		{[]string{"lock"}, []string{"bob lock [--update [names…]] [--nixpkgs <rev>] [--offline]"}},
		{[]string{"graph"}, []string{"bob graph [patterns…] [--why <from> <to>] [--rdeps] [--depth N] [--format text|dot|json]"}},
		{[]string{"log"}, []string{"bob log <label> [--build <id>] [-f] [--attempt N]"}},
		{[]string{"cancel"}, []string{"bob cancel <build-id>"}},
		{[]string{"pipeline"}, []string{"bob pipeline run <name> [--remote]"}},
		{[]string{"login"}, []string{"bob login [<url>] [--device] [--token-stdin]"}},
		{[]string{"logout"}, []string{"bob logout [<url>]"}},
		{[]string{"install"}, []string{"bob install <ref> [--as <name>] [--bin-dir <dir>]"}},
		{[]string{"uninstall"}, nil},
		{[]string{"upgrade"}, nil},
		{[]string{"rollback"}, nil},
		{[]string{"list"}, nil},
		{[]string{"registry"}, []string{"bob registry add <name> <url> | remove <name> | list | default <name>"}},
		{[]string{"completion"}, []string{"bob completion bash|zsh|fish"}},
		{[]string{"gc"}, []string{"bob gc [--dry-run]"}},
		{[]string{"doctor"}, []string{"bob doctor [--repair]"}},
		{[]string{"version"}, []string{"bob version [--json]"}},
	}
}

func (c command) name() string { return strings.Join(c.Args, " ") }

// usageLines returns the stdout lines that start with "usage: bob <cmd>".
func usageLines(stdout, cmd string) []string {
	var out []string
	for _, l := range lines(stdout) {
		if l == "usage: bob "+cmd || strings.HasPrefix(l, "usage: bob "+cmd+" ") {
			out = append(out, l)
		}
	}
	return out
}

// TestRCLI001CommandSet: every command of the table answers `--help` and `help <cmd>` with exit
// 0 and a `usage: bob <cmd>` line that matches the table; the output is golden (HARNESS §13.1).
func TestRCLI001CommandSet(t *testing.T) {
	parity.Covers(t, "R-CLI-001")
	for _, c := range commandTable() {
		t.Run(c.name(), func(t *testing.T) {
			env := home(t)
			for _, form := range [][]string{append(append([]string{}, c.Args...), "--help"), append([]string{"help"}, c.Args...)} {
				r := bob(t, t.TempDir(), env, form...)
				if r.Exit != 0 {
					t.Fatalf("bob %s: exit %d, want 0\n%s", strings.Join(form, " "), r.Exit, r.Combined())
				}
				u := usageLines(r.Stdout, c.name())
				if len(u) == 0 {
					t.Fatalf("bob %s: no `usage: bob %s` line in stdout:\n%s", strings.Join(form, " "), c.name(), r.Stdout)
				}
				for _, syn := range c.Synopsis {
					found := false
					for _, l := range u {
						found = found || l == "usage: "+syn
					}
					if !found {
						t.Errorf("bob %s: usage line does not match the table\n want: usage: %s\n  got: %q", strings.Join(form, " "), syn, u)
					}
				}
				noLeak(t, r)
				if form[len(form)-1] == "--help" {
					golden.Assert(t, "tests/goldens/cli/help/"+strings.ReplaceAll(c.name(), " ", "-")+".txt", []byte(r.Stdout), golden.Reqs("R-CLI-001"))
				}
			}
		})
	}
}

// TestRCLI001UnknownCommand: `bob frobnicate` exits 2 with BOB-E-0001; `did you mean …?` appears
// exactly when a command is within edit distance 2.
func TestRCLI001UnknownCommand(t *testing.T) {
	parity.Covers(t, "R-CLI-001")
	env := home(t)
	r := bob(t, t.TempDir(), env, "frobnicate")
	if r.Exit != 2 {
		t.Errorf("bob frobnicate: exit %d, want 2", r.Exit)
	}
	if !strings.Contains(r.Combined(), "BOB-E-0001") {
		t.Errorf("bob frobnicate: no BOB-E-0001 in output:\n%s", r.Combined())
	}
	if strings.Contains(r.Combined(), "did you mean") {
		t.Errorf("bob frobnicate: suggests a command although none is within edit distance 2:\n%s", r.Combined())
	}
	for typo, want := range map[string]string{"buil": "build", "tset": "test", "chek": "check", "grahp": "graph", "bulid": "build", "instal": "install"} {
		r := bob(t, t.TempDir(), env, typo)
		if r.Exit != 2 || !strings.Contains(r.Combined(), "BOB-E-0001") {
			t.Errorf("bob %s: exit %d, want 2 with BOB-E-0001:\n%s", typo, r.Exit, r.Combined())
		}
		if !strings.Contains(r.Combined(), "did you mean "+want) {
			t.Errorf("bob %s: want a `did you mean %s?` hint:\n%s", typo, want, r.Combined())
		}
		noLeak(t, r)
	}
}
