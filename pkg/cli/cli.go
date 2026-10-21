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
	if !ok {
		fmt.Fprintf(stderr, "error[BOB-E-0001]: unknown command %q\n  hint: run bob help\n", name)
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
