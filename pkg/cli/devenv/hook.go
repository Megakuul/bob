package devenv

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Variables of the shell hook (SPEC §02.7.1).
const (
	VarProject    = "BOB_PROJECT"
	VarEnvHash    = "BOB_ENV_HASH"
	VarRestore    = "BOB_ENV_RESTORE"
	VarDeniedSeen = "BOB_DENIED_SEEN"
	VarDev        = "BOB_DEV"
)

// Hook returns the shell hook script of SPEC §02.7.1 for shell ("bash", "zsh" or "fish");
// bobPath is the absolute path of the running bob that the script embeds.
func Hook(shell, bobPath string) (string, error) {
	var s string
	switch shell {
	case "bash":
		s = `# bob shell hook (bash)
_bob_hook() {
  local s=$?
  eval "$("<bob>" env --hook=bash)"
  return $s
}
if [[ ";${PROMPT_COMMAND[*]:-};" != *";_bob_hook;"* ]]; then
  if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
    PROMPT_COMMAND=(_bob_hook "${PROMPT_COMMAND[@]}")
  else
    PROMPT_COMMAND="_bob_hook${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
  fi
fi
`
	case "zsh":
		s = `# bob shell hook (zsh)
_bob_hook() { eval "$("<bob>" env --hook=zsh)" }
typeset -ag precmd_functions chpwd_functions
(( ${precmd_functions[(I)_bob_hook]} )) || precmd_functions=(_bob_hook $precmd_functions)
(( ${chpwd_functions[(I)_bob_hook]} )) || chpwd_functions=(_bob_hook $chpwd_functions)
`
	case "fish":
		s = `# bob shell hook (fish)
function __bob_hook --on-event fish_prompt --on-variable PWD
    "<bob>" env --hook=fish | source
end
`
	default:
		return "", diag.New(1, "kind", "shell", "name", shell, "suggest", "bash, zsh or fish")
	}
	return strings.ReplaceAll(s, "<bob>", dquoteEsc(bobPath)), nil
}

// Script is the output of bob env: commands for one shell that change its environment. It
// keeps a simulated copy of the environment so leave and enter compose (SPEC §02.7.1 step 6).
type Script struct {
	shell string // "sh" (bash, zsh, POSIX) or "fish"
	env   map[string]string
	b     strings.Builder
}

// NewScript starts a script for shell ("bash", "zsh", "sh" or "fish") over the environment env
// (KEY=VALUE entries).
func NewScript(shell string, env []string) *Script {
	s := &Script{shell: "sh", env: map[string]string{}}
	if shell == "fish" {
		s.shell = "fish"
	}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			s.env[k] = v
		}
	}
	return s
}

// String is the script text.
func (s *Script) String() string { return s.b.String() }

// Get returns a variable of the simulated environment.
func (s *Script) Get(k string) (string, bool) { v, ok := s.env[k]; return v, ok }

// Set exports k=v.
func (s *Script) Set(k, v string) {
	s.env[k] = v
	if s.shell == "fish" {
		if k == "PATH" {
			s.b.WriteString("set -gx PATH")
			for _, e := range strings.Split(v, ":") {
				s.b.WriteString(" " + fishQuote(e))
			}
			s.b.WriteString(";\n")
			return
		}
		s.b.WriteString("set -gx " + k + " " + fishQuote(v) + ";\n")
		return
	}
	s.b.WriteString("export " + k + "=" + Quote(v) + ";\n")
}

// Unset removes k if it is set.
func (s *Script) Unset(k string) {
	if _, ok := s.env[k]; !ok {
		return
	}
	delete(s.env, k)
	if s.shell == "fish" {
		s.b.WriteString("set -e " + k + ";\n")
		return
	}
	s.b.WriteString("unset " + k + ";\n")
}

// fishQuote is fish single quoting: only \ and ' are escaped.
func fishQuote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// ShimDir is the shim directory of root.
func ShimDir(root string) string { return filepath.Join(root, ".bob", "bin") }

// Leave restores the environment of the active project (BOB_PROJECT): the variables saved in
// BOB_ENV_RESTORE get their old values back, the shim directory leaves PATH, and the hook's
// own variables are unset. Without an active project it does nothing.
func (s *Script) Leave() {
	active, ok := s.env[VarProject]
	if !ok {
		return
	}
	saved := decodeRestore(s.env[VarRestore])
	keys := make([]string, 0, len(saved))
	for k := range saved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "PATH" {
			continue
		}
		if v := saved[k]; v == nil {
			s.Unset(k)
		} else {
			s.Set(k, *v)
		}
	}
	// PATH keeps changes the user made inside the project; only the shims leave.
	if p, ok := s.env["PATH"]; ok {
		shim := ShimDir(active)
		var keep []string
		for _, e := range strings.Split(p, ":") {
			if e != shim {
				keep = append(keep, e)
			}
		}
		rest := strings.Join(keep, ":")
		if old, had := saved["PATH"]; had && old == nil && rest == "" {
			s.Unset("PATH")
		} else if rest != p {
			s.Set("PATH", rest)
		}
	}
	s.Unset(VarProject)
	s.Unset(VarEnvHash)
	s.Unset(VarRestore)
}

// Enter activates root with env file f (whose bytes hash to hash): it saves the values it is
// about to change in BOB_ENV_RESTORE, puts the shims first on PATH, exports outside.set and
// sets BOB_PROJECT and BOB_ENV_HASH.
func (s *Script) Enter(root string, f *File, hash string) {
	vars := []string{"PATH"}
	keys := make([]string, 0, len(f.Outside.Set))
	for k := range f.Outside.Set {
		if validName(k) && k != "PATH" && !strings.HasPrefix(k, "BOB_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	vars = append(vars, keys...)
	saved := map[string]*string{}
	for _, k := range vars {
		if v, ok := s.env[k]; ok {
			saved[k] = &v
		} else {
			saved[k] = nil
		}
	}
	b, _ := json.Marshal(saved)
	s.Set(VarRestore, base64.RawURLEncoding.EncodeToString(b))
	path := ShimDir(root)
	if p := s.env["PATH"]; p != "" {
		path += ":" + p
	}
	s.Set("PATH", path)
	for _, k := range keys {
		s.Set(k, f.Outside.Set[k])
	}
	s.Set(VarProject, root)
	s.Set(VarEnvHash, hash)
}

func decodeRestore(v string) map[string]*string {
	m := map[string]*string{}
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(b) > 1<<20 {
		return m
	}
	if json.Unmarshal(b, &m) != nil {
		return map[string]*string{}
	}
	for k := range m {
		if !validName(k) {
			delete(m, k)
		}
	}
	return m
}

// validName reports whether k is a portable variable name (so it can be written unquoted).
func validName(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// DeniedSeen decodes BOB_DENIED_SEEN: the roots whose "not allowed" line this shell printed.
func DeniedSeen(v string) []string {
	var roots []string
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &roots) != nil {
		return nil
	}
	return roots
}

// EncodeDeniedSeen is BOB_DENIED_SEEN of roots.
func EncodeDeniedSeen(roots []string) string {
	b, _ := json.Marshal(roots)
	return base64.RawURLEncoding.EncodeToString(b)
}
