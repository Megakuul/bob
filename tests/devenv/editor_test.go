package devenv

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/parity"
)

// decode reads a JSON object file or text.
func decode(t testing.TB, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, b)
	}
	return m
}

func golden(t testing.TB, name string) map[string]any {
	t.Helper()
	return decode(t, []byte(readFile(t, filepath.Join("testdata", "golden", name))))
}

// checkSettings verifies that got is exactly the unrelated keys plus the golden keys.
func checkSettings(t testing.TB, what string, got map[string]any, unrelated map[string]any, want map[string]any) {
	t.Helper()
	exp := map[string]any{}
	for k, v := range unrelated {
		exp[k] = v
	}
	for k, v := range want {
		exp[k] = v
	}
	if !reflect.DeepEqual(got, exp) {
		gj, _ := json.MarshalIndent(got, "", "  ")
		ej, _ := json.MarshalIndent(exp, "", "  ")
		t.Errorf("%s: settings differ from the golden\n got: %s\nwant: %s", what, gj, ej)
	}
}

// TestRDEV011EditorSettings: bob editor vscode --write keeps an unrelated key and adds exactly
// the SPEC §02.7.6 keys for the ecosystems in use; running it twice gives a byte-identical file;
// a Go-only project gets no rust-analyzer.* or clangd.* key; printing without --write does not
// touch the file; a file with comments is refused with BOB-E-0004. The golden is
// testdata/golden/vscode-*.json (the SPEC block, compared by value). (manual) VS Code with the
// Go extension: go-to-definition on fmt.Println opens print.go.
func TestRDEV011EditorSettings(t *testing.T) {
	parity.Covers(t, "R-DEV-011")
	unrelated := map[string]any{"editor.fontSize": json.Number("14"), "files.exclude": map[string]any{"**/.git": true}}
	seed := `{
  "editor.fontSize": 14,
  "files.exclude": { "**/.git": true }
}
`
	t.Run("go+rust+c", func(t *testing.T) {
		w := newWorld(t)
		dir := w.fixture("acme")
		settings := filepath.Join(dir, ".vscode", "settings.json")
		writeFile(t, settings, seed)
		w.acc.chownTree(t, dir)

		// Printing changes nothing and shows the settings.
		r := w.bob(dir, "editor", "vscode")
		if redIf(t, r) || !exitIs(t, r, 0) {
			return
		}
		noLeak(t, r)
		if readFile(t, settings) != seed {
			t.Errorf("bob editor vscode (without --write) changed .vscode/settings.json")
		}
		if !strings.Contains(r.Stdout, `${workspaceFolder}/.bob/bin/gopls`) {
			t.Errorf("bob editor vscode printed no settings:\n%s", r.Stdout)
		}

		r = w.bob(dir, "editor", "vscode", "--write")
		if redIf(t, r) || !exitIs(t, r, 0) {
			return
		}
		first := readFile(t, settings)
		checkSettings(t, "first --write", decode(t, []byte(first)), unrelated, golden(t, "vscode-all.json"))

		r = w.bob(dir, "editor", "vscode", "--write")
		if redIf(t, r) || !exitIs(t, r, 0) {
			return
		}
		if second := readFile(t, settings); second != first {
			t.Errorf("a second --write changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
	t.Run("go-only", func(t *testing.T) {
		w := newWorld(t)
		dir := w.local("goonly")
		settings := filepath.Join(dir, ".vscode", "settings.json")
		writeFile(t, settings, seed)
		w.acc.chownTree(t, dir)
		r := w.bob(dir, "editor", "vscode", "--write")
		if redIf(t, r) || !exitIs(t, r, 0) {
			return
		}
		got := decode(t, []byte(readFile(t, settings)))
		checkSettings(t, "go-only", got, unrelated, golden(t, "vscode-go-only.json"))
		for k := range got {
			if strings.HasPrefix(k, "rust-analyzer.") || strings.HasPrefix(k, "clangd.") {
				t.Errorf("a Go-only project got the key %q", k)
			}
		}
	})
	t.Run("no settings file yet", func(t *testing.T) {
		w := newWorld(t)
		dir := w.local("goonly")
		r := w.bob(dir, "editor", "vscode", "--write")
		if redIf(t, r) || !exitIs(t, r, 0) {
			return
		}
		checkSettings(t, "new file", decode(t, []byte(readFile(t, filepath.Join(dir, ".vscode", "settings.json")))), nil, golden(t, "vscode-go-only.json"))
	})
	t.Run("comments are refused", func(t *testing.T) {
		w := newWorld(t)
		dir := w.local("goonly")
		settings := filepath.Join(dir, ".vscode", "settings.json")
		commented := "{\n  // my font\n  \"editor.fontSize\": 14\n}\n"
		writeFile(t, settings, commented)
		w.acc.chownTree(t, dir)
		r := w.bob(dir, "editor", "vscode", "--write")
		if redIf(t, r) {
			return
		}
		noLeak(t, r)
		if r.Exit == 0 || !strings.Contains(r.Stderr, "BOB-E-0004") {
			t.Errorf("commented settings.json: exit %d, stderr %q, want a failure with BOB-E-0004", r.Exit, r.Stderr)
		}
		if !strings.Contains(r.Stderr, "go.alternateTools") {
			t.Errorf("BOB-E-0004 does not show the snippet to paste by hand:\n%s", r.Stderr)
		}
		if b, _ := os.ReadFile(settings); string(b) != commented {
			t.Errorf("the refused file was modified: %q", b)
		}
	})
}
