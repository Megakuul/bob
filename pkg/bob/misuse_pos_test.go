package bob

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A definition error recorded through ext.Errorf as "<file>:<line>: <label>: <text>" carries
// that position and label on its diagnostic line (I-M7a NEEDS 3).
func TestMisuseCarriesPosition(t *testing.T) {
	ev := &evaluation{}
	ev.misuse(`libs/mathx/bob.go:12: //libs/mathx:mathx: flag "-I/nix/store/x" is not allowed`)
	ev.misuse(`api/bob.go:3: npm.TestOpts.Of is required`)
	ev.misuse(`ext.Services: no position here`)
	var b bytes.Buffer
	writeDiags(&b, ev.diags)
	type line struct {
		File  string `json:"file"`
		Line  int    `json:"line"`
		Label string `json:"label"`
		Msg   string `json:"msg"`
	}
	want := []line{
		{"libs/mathx/bob.go", 12, "//libs/mathx:mathx", `libs/mathx/bob.go:12: //libs/mathx:mathx: flag "-I/nix/store/x" is not allowed`},
		{"api/bob.go", 3, "", "api/bob.go:3: npm.TestOpts.Of is required"},
		{"", 0, "", "ext.Services: no position here"},
	}
	got := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(got), len(want), b.String())
	}
	for i, l := range got {
		var g line
		if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "bob:: ")), &g); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if g != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, g, want[i])
		}
	}
}

// A last line "hint: <text>" of an ext.Errorf message is the diagnostic's hint, not part of its
// message (BOB-E-1020 "{hint}", fault 85).
func TestMisuseHint(t *testing.T) {
	ev := &evaluation{}
	ev.misuse("app/bob.go:16: //app:app: flag \"-I../x\" is not allowed; use Deps to add include paths\nhint: depend on the library")
	var b bytes.Buffer
	writeDiags(&b, ev.diags)
	var g struct {
		Msg, Hint, Label string
		Line             int
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(b.String()), "bob:: ")), &g); err != nil {
		t.Fatal(err)
	}
	if g.Msg != `app/bob.go:16: //app:app: flag "-I../x" is not allowed; use Deps to add include paths` ||
		g.Hint != "depend on the library" || g.Label != "//app:app" || g.Line != 16 {
		t.Errorf("diagnostic %+v", g)
	}
}
