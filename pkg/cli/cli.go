package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// command is one entry of the command table (SPEC §02.1).
type command struct {
	name     string
	synopsis []string
	summary  string
}

// commands is the user command set of SPEC §02.1. It has no server command (R-ARCH-004).
var commands = []command{
	{"build", []string{"bob build [patterns…] [--remote] [--platform P] [--variant V] [--print-paths] [--keep-going=true] [--lock]"}, "build packages"},
	{"test", []string{"bob test [patterns…] [--remote] [--rerun] [--lock] [--require-all-tests] [-- test args]"}, "run tests"},
	{"run", []string{"bob run <label> [--platform native] [-- args]"}, "build and run a binary"},
	{"dev", []string{"bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]"}, "enter the developer environment"},
	{"env", []string{"bob env [--shell <name>] [--format sh|fish|json] [--hook=bash|zsh|fish]"}, "print the developer environment"},
	{"hook", []string{"bob hook bash|zsh|fish"}, "print the shell hook"},
	{"allow", []string{"bob allow [dir]"}, "allow the shell hook in a directory"},
	{"deny", []string{"bob deny [dir]"}, "deny the shell hook in a directory"},
	{"exec", []string{"bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]"}, "run a tool of the developer environment"},
	{"editor", []string{"bob editor vscode|nvim|helix|zed|jetbrains [--write]"}, "set up an editor"},
	{"check", []string{"bob check [--fix]"}, "check bob.go files and bob.lock"},
	{"gen", []string{"bob gen [patterns…] [--check] [--force]"}, "write native tooling files and generated code"},
	{"status", []string{"bob status [--json]"}, "show the state of generated files and the lock"},
	{"lock", []string{"bob lock [--update [names…]] [--nixpkgs <rev>] [--offline]"}, "update bob.lock"},
	{"graph", []string{"bob graph [patterns…] [--why <from> <to>] [--rdeps] [--depth N] [--format text|dot|json]"}, "show the package graph"},
	{"log", []string{"bob log <label> [--build <id>] [-f] [--attempt N]"}, "show a build log"},
	{"cancel", []string{"bob cancel <build-id>"}, "cancel a remote build"},
	{"pipeline", []string{"bob pipeline run <name> [--remote]"}, "run a pipeline"},
	{"login", []string{"bob login [<url>] [--device] [--token-stdin]"}, "log in to a bob server"},
	{"logout", []string{"bob logout [<url>]"}, "log out of a bob server"},
	{"install", []string{"bob install <ref> [--as <name>] [--bin-dir <dir>]"}, "install a release"},
	{"uninstall", []string{"bob uninstall <ref>"}, "remove an installed release"},
	{"upgrade", []string{"bob upgrade"}, "upgrade installed releases"},
	{"rollback", []string{"bob rollback"}, "go back to the previous install generation"},
	{"list", []string{"bob list"}, "list installed releases"},
	{"registry", []string{"bob registry add <name> <url> | remove <name> | list | default <name>"}, "manage release registries"},
	{"completion", []string{"bob completion bash|zsh|fish"}, "print shell completion"},
	{"gc", []string{"bob gc [--dry-run]"}, "collect garbage in bob's store"},
	{"doctor", []string{"bob doctor [--repair]"}, "check and repair bob's setup"},
	{"version", []string{"bob version [--json]"}, "print version information"},
}

// Main runs the bob command line with args (without the program name) and returns the exit
// status.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[0]) {
		printHelp(stdout)
		return 0
	}
	name := args[0]
	if name == "help" {
		if len(args) == 1 {
			printHelp(stdout)
			return 0
		}
		name = args[1]
		args = []string{name, "--help"}
	}
	cmd, ok := lookup(name)
	if name == "__complete" { // hidden: the target of the completion scripts (R-CLI-004)
		cmd, ok = command{name: name}, true
	}
	if !ok {
		hint := "run bob help"
		if s := suggest(name); s != "" {
			hint = "did you mean " + s + "?"
		}
		fmt.Fprintf(stderr, "error[BOB-E-0001]: unknown command %q\n  hint: %s\n", name, hint)
		return 2
	}
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		if isHelp(a) {
			for _, s := range cmd.synopsis {
				fmt.Fprintf(stdout, "usage: %s\n", s)
			}
			return 0
		}
	}
	err := run(cmd)
	var d *diag.Error
	if errors.As(err, &d) {
		fmt.Fprintf(stderr, "error[BOB-E-%04d]: %s\n", uint16(d.Code), d.Error())
		return diag.ExitCode([]*diag.Error{d})
	}
	return 0
}

// run executes a command. No command is implemented yet.
func run(cmd command) error {
	return diag.NotImplemented("bob " + cmd.name)
}

func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func isHelp(a string) bool { return a == "--help" }

func printHelp(w io.Writer) {
	var b strings.Builder
	b.WriteString("usage: bob [global flags] <command> [subcommand] [flags] [args] [-- passthrough args]\n\ncommands:\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-11s %s\n", c.name, c.summary)
	}
	b.WriteString("\nrun 'bob <command> --help' for a command's flags\n")
	io.WriteString(w, b.String())
}

// suggest returns the command closest to name within edit distance 2 (SPEC R-CLI-001), or "".
func suggest(name string) string {
	best, bestD := "", 3
	for _, c := range commands {
		if d := editDistance(name, c.name); d < bestD {
			best, bestD = c.name, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance of a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
