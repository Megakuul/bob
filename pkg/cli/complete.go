package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// labelIndex is the label index of the last evaluation (SPEC §02.9): one label per line.
// Completion reads it and never evaluates.
const labelIndex = ".bob/labels"

// maxLabelIndex bounds the label index bob reads for completion.
const maxLabelIndex = 16 << 20

// recordLabels merges labels into the label index of root. Failures are ignored: the index
// only serves completion.
func recordLabels(root string, labels []string) {
	if len(labels) == 0 {
		return
	}
	p := filepath.Join(root, filepath.FromSlash(labelIndex))
	set := map[string]bool{}
	for _, l := range readLabels(root) {
		set[l] = true
	}
	for _, l := range labels {
		if l != "" && !strings.ContainsAny(l, "\n\r") {
			set[l] = true
		}
	}
	all := make([]string, 0, len(set))
	for l := range set {
		all = append(all, l)
	}
	sort.Strings(all)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(strings.Join(all, "\n")+"\n"), 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

func readLabels(root string) []string {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(labelIndex)))
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() > maxLabelIndex {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// flagValues are the fixed value sets completion offers after a flag, by command ("" = any).
func flagValues(cmd, flag string) []string {
	switch flag {
	case "--format":
		if cmd == "graph" {
			return []string{"text", "dot", "json"}
		}
		return []string{"sh", "fish", "json"}
	case "--hook":
		return []string{"bash", "zsh", "fish"}
	case "--color":
		return []string{"auto", "always", "never"}
	}
	return nil
}

// takesLabels reports whether a command takes labels or label patterns as arguments.
func takesLabels(cmd string) bool {
	switch cmd {
	case "build", "test", "run", "graph", "log", "gen":
		return true
	}
	return false
}

// runComplete is the hidden `bob __complete <words…>` (SPEC R-CLI-004): the candidates for
// the last word, one per line. It never evaluates and never enters the namespace.
func runComplete(words []string, cwd string, w func(string)) {
	if len(words) == 0 {
		words = []string{""}
	}
	cur := words[len(words)-1]
	emit := func(cands []string) {
		for _, c := range cands {
			if strings.HasPrefix(c, cur) {
				w(c + "\n")
			}
		}
	}
	if len(words) == 1 {
		var names []string
		for _, c := range commands() {
			names = append(names, c.name)
		}
		names = append(names, "help")
		emit(names)
		return
	}
	cmd, ok := lookup(words[0])
	if !ok {
		if words[0] == "help" && len(words) == 2 {
			var names []string
			for _, c := range commands() {
				names = append(names, c.name)
			}
			emit(names)
		}
		return
	}
	prev := words[len(words)-2]
	if prev == "--shell" || strings.HasPrefix(cur, "--shell=") {
		// dev shell names of the last evaluation (.bob/shells); never evaluates
		if root, err := findRoot(cwd, false); err == nil {
			if v, ok := strings.CutPrefix(cur, "--shell="); ok {
				for _, n := range readShells(root) {
					if strings.HasPrefix(n, v) {
						w("--shell=" + n + "\n")
					}
				}
				return
			}
			emit(readShells(root))
		}
		return
	}
	if cmd.name == "exec" && slices.Contains(words[:len(words)-1], "--") {
		// the tool after "--": the shims of the project
		if words[len(words)-2] == "--" {
			if root, err := findRoot(cwd, false); err == nil {
				emit(shimNames(root))
			}
		}
		return
	}
	if strings.HasPrefix(prev, "--") && !strings.Contains(prev, "=") {
		if v := flagValues(cmd.name, prev); v != nil {
			emit(v)
			return
		}
	}
	if name, val, ok := strings.Cut(cur, "="); ok && strings.HasPrefix(name, "--") {
		for _, v := range flagValues(cmd.name, name) {
			if strings.HasPrefix(v, val) {
				w(name + "=" + v + "\n")
			}
		}
		return
	}
	if strings.HasPrefix(cur, "-") {
		var fl []string
		for _, f := range append(append([]flag{}, cmd.flags...), globalFlags...) {
			if f.short {
				fl = append(fl, "-"+f.name)
			} else {
				fl = append(fl, "--"+f.name)
			}
		}
		emit(fl)
		return
	}
	switch cmd.name {
	case "hook", "completion":
		if len(words) == 2 {
			emit([]string{"bash", "zsh", "fish"})
		}
		return
	case "editor":
		if len(words) == 2 {
			emit([]string{"vscode", "nvim", "helix", "zed", "jetbrains"})
		}
		return
	case "registry":
		if len(words) == 2 {
			emit([]string{"add", "remove", "list", "default"})
		} else if len(words) == 3 && (words[1] == "remove" || words[1] == "default") {
			emit(registryNames())
		}
		return
	case "uninstall", "upgrade":
		emit(installedNames())
		return
	case "pipeline":
		if len(words) == 2 {
			emit([]string{"run"})
		}
		return
	}
	if !takesLabels(cmd.name) {
		return
	}
	root, err := findRoot(cwd, false)
	if err != nil {
		return
	}
	emit(readLabels(root))
}

// completionScript is the script of `bob completion <shell>`; each calls the hidden
// `bob __complete` with the words typed so far.
func completionScript(shell string) (string, bool) {
	switch shell {
	case "bash":
		// The words are split from the line itself: bash's own COMP_WORDS breaks labels at
		// ':' (COMP_WORDBREAKS), and the part before the last ':' is taken off the candidates.
		return `# bob completion for bash: source <(bob completion bash)
_bob_complete() {
    local line="${COMP_LINE:0:$COMP_POINT}"
    local -a words
    read -r -a words <<< "$line"
    [[ "$line" == *[[:space:]] ]] && words+=("")
    local cur="${words[${#words[@]}-1]}"
    local IFS=$'\n'
    local -a cands
    cands=($(bob __complete "${words[@]:1}" 2>/dev/null))
    if [[ "$cur" == *:* && "$COMP_WORDBREAKS" == *:* ]]; then
        local pre="${cur%"${cur##*:}"}"
        cands=("${cands[@]#"$pre"}")
    fi
    COMPREPLY=("${cands[@]}")
}
complete -o default -F _bob_complete bob
`, true
	case "zsh":
		return `#compdef bob
# bob completion for zsh: source <(bob completion zsh)
_bob_complete() {
    local -a cands
    cands=(${(f)"$(bob __complete "${(@)words[2,CURRENT]}" 2>/dev/null)"})
    compadd -Q -- "${cands[@]}"
}
compdef _bob_complete bob
`, true
	case "fish":
		return `# bob completion for fish: bob completion fish | source
function __bob_complete
    set -l tokens (commandline -opc)
    bob __complete $tokens[2..-1] "$(commandline -ct)" 2>/dev/null
end
complete -c bob -f -a '(__bob_complete)'
`, true
	}
	return "", false
}

// runCompletion is `bob completion bash|zsh|fish`.
func runCompletion(e *env) int {
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob completion", "what", "a shell (bash, zsh or fish)"))
	}
	s, ok := completionScript(e.a.pos[0])
	if !ok {
		return e.out.fail(diag.New(1, "kind", "shell", "name", e.a.pos[0], "suggest", "bash, zsh or fish"))
	}
	if e.out.json {
		e.out.result(struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}{e.a.pos[0], s})
		return e.out.finish(0, translate.Counts{}, nil)
	}
	e.out.printf("%s", s)
	return 0
}

// shimNames are the tools of the project's dev shell (.bob/bin), for `bob exec -- <tool>`.
func shimNames(root string) []string {
	ents, err := os.ReadDir(filepath.Join(root, ".bob", "bin"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}
