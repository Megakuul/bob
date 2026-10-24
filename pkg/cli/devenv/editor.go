package devenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Editors are the editors bob editor knows (SPEC §02.7.6).
var Editors = []string{"vscode", "nvim", "helix", "zed", "jetbrains"}

// Settings is what bob editor prints and writes for one editor.
type Settings struct {
	File   string         // project-relative settings file ("" = nothing to write)
	Values map[string]any // top-level keys merged into File
	Note   string         // what to do instead / in addition (human output)
}

// EditorSettings returns the settings of editor for a project that uses the ecosystems ecos
// (bob.lock section names: "go", "rust", "cc", "npm", "ocaml"). Only keys of ecosystems in use
// are included (R-DEV-011).
func EditorSettings(editor string, ecos []string) (Settings, error) {
	uses := map[string]bool{}
	for _, e := range ecos {
		uses[e] = true
	}
	shim := func(tool string) string { return "${workspaceFolder}/.bob/bin/" + tool }
	switch editor {
	case "vscode":
		v := map[string]any{"files.watcherExclude": map[string]any{"**/.bob/**": true}}
		if uses["go"] {
			v["go.alternateTools"] = map[string]any{"go": shim("go"), "gopls": shim("gopls")}
			v["go.toolsManagement.autoUpdate"] = false
			v["gopls"] = map[string]any{"build.buildFlags": []any{"-tags=bob"}, "build.directoryFilters": []any{"-.bob"}}
		}
		if uses["rust"] {
			v["rust-analyzer.server.path"] = shim("rust-analyzer")
		}
		if uses["cc"] {
			v["clangd.path"] = shim("clangd")
		}
		note := ""
		if uses["npm"] {
			note = "VS Code's built-in TypeScript reads node_modules itself: start it with bob dev -- code --new-window --user-data-dir .bob/vscode ."
		}
		return Settings{File: ".vscode/settings.json", Values: v, Note: note}, nil
	case "zed":
		lsp := map[string]any{}
		for _, s := range []struct{ eco, server, tool string }{
			{"go", "gopls", "gopls"}, {"rust", "rust-analyzer", "rust-analyzer"}, {"cc", "clangd", "clangd"},
			{"npm", "typescript-language-server", "typescript-language-server"}, {"ocaml", "ocamllsp", "ocamllsp"},
		} {
			if uses[s.eco] {
				lsp[s.server] = map[string]any{"binary": map[string]any{"path": ".bob/bin/" + s.tool}}
			}
		}
		return Settings{File: ".zed/settings.json", Values: map[string]any{"lsp": lsp},
			Note: "Zed finds the language servers on PATH with the shell hook; these entries are for Zed builds that ignore PATH."}, nil
	case "nvim", "helix":
		return Settings{Note: "nothing to configure: with the shell hook (bob hook) the language servers resolve to .bob/bin on PATH"}, nil
	case "jetbrains":
		return Settings{Note: "JetBrains IDEs read SDK and toolchain files themselves: start the IDE with bob dev -- <ide> ."}, nil
	}
	return Settings{}, diag.New(1, "kind", "editor", "name", editor, "suggest", strings.Join(Editors, ", "))
}

// Snippet is the settings as indented JSON (what bob editor prints).
func (s Settings) Snippet() string {
	b, _ := encodeJSON(s.Values)
	return string(b)
}

func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// maxSettings bounds an editor settings file bob merges into.
const maxSettings = 8 << 20

// Merge merges s into root/s.File: top-level keys of s replace the file's, every other key is
// kept. A file that is not plain JSON (comments, trailing commas) is refused with BOB-E-0004;
// the caller shows the snippet to paste by hand. It reports whether the file changed.
func (s Settings) Merge(root string) (bool, error) {
	if s.File == "" {
		return false, nil
	}
	p := filepath.Join(root, filepath.FromSlash(s.File))
	cur := map[string]any{}
	b, err := readCapped(p, maxSettings)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if len(bytes.TrimSpace(b)) > 0 {
			if err := dec.Decode(&cur); err != nil || cur == nil {
				return false, diag.New(4, "flag", s.File, "what", "to be plain JSON without comments for --write; paste the settings by hand")
			}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return false, err
	}
	keys := make([]string, 0, len(s.Values))
	for k := range s.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cur[k] = s.Values[k]
	}
	out, err := encodeJSON(cur)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm()
	}
	return WriteFile(p, out, mode)
}
