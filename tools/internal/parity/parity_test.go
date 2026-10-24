package parity

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The generator must list every requirement in SPEC.md (M0.4).
func TestGeneratorListsAllSpecIDs(t *testing.T) {
	root := repoRoot(t)
	f, err := os.Open(filepath.Join(root, SpecPath))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reqs, err := ParseSpec(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 252 {
		t.Fatalf("got %d requirements, want 252", len(reqs))
	}
	cat := Build(root, reqs, nil, nil, nil)
	md := cat.Markdown()
	byID := map[string]Requirement{}
	for _, r := range reqs {
		byID[r.ID] = r
		if !strings.Contains(md, "| "+r.ID+" | ") {
			t.Errorf("docs/parity.md lacks %s", r.ID)
		}
	}
	// Headings with two ids yield two entries with the same title.
	for _, id := range []string{"R-FW-012", "R-FW-014", "R-FLEET-008", "R-SEC-000", "R-GO-002b"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}
	if byID["R-FW-012"].Title != byID["R-FW-014"].Title {
		t.Errorf("R-FW-012 and R-FW-014 should share a title")
	}
	if !strings.Contains(md, "| **all** | 252 |") {
		t.Errorf("totals row does not count 252 requirements")
	}
}

func TestParseSpec(t *testing.T) {
	spec := `## 1.1 Layout
#### R-A-001 First
- **Acceptance:** a test.
### 1.1.1 Sub
#### R-A-002 / R-B-003 Two ids
- **Acceptance:** (manual) review.
#### R-A-004a Suffix
- **Acceptance (manual):** runbook.
#### Not a requirement
`
	got, err := ParseSpec(strings.NewReader(spec))
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{
		{ID: "R-A-001", Area: "A", Title: "First", Section: "1.1 Layout"},
		{ID: "R-A-002", Area: "A", Title: "Two ids", Section: "1.1.1 Sub", Manual: true},
		{ID: "R-B-003", Area: "B", Title: "Two ids", Section: "1.1.1 Sub", Manual: true},
		{ID: "R-A-004a", Area: "A", Title: "Suffix", Section: "1.1.1 Sub", Manual: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"#### R-A-1 x\n", "#### R-A-001 x\n#### R-A-001 y\n"} {
		if _, err := ParseSpec(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseSpec(%q): want error", bad)
		}
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tests/fw/a_test.go", `package fw
import (
	"testing"
	p "github.com/megakuul/bob/tests/parity"
)
func TestA(t *testing.T) { p.Covers(t, "R-FW-002", "R-FW-001") }
func TestB(t *testing.T) {
	t.Run("x", func(t *testing.T) { p.Covers(t, "R-FW-003") })
}
`)
	write("tests/fw/other_test.go", `package fw
import "testing"
type x struct{}
func (x) Covers(t *testing.T, s string) {}
func TestC(t *testing.T) { var parity x; parity.Covers(t, "R-FW-009") }
`)
	write("out/x_test.go", `package x; import "github.com/megakuul/bob/tests/parity"; func TestZ(t T) { parity.Covers(t, "R-Z-001") }`)
	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"tests/fw.TestA": {"R-FW-001", "R-FW-002"},
		"tests/fw.TestB": {"R-FW-003"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	write("tests/bad/b_test.go", `package bad
import "github.com/megakuul/bob/tests/parity"
func TestBad(t T) { id := "R-FW-001"; parity.Covers(t, id) }
`)
	if _, err := Scan(context.Background(), root); err == nil {
		t.Errorf("non-literal id: want error")
	}
}

func TestBuildStatus(t *testing.T) {
	root := "/repo"
	reqs := []Requirement{
		{ID: "R-A-001"}, {ID: "R-A-002"}, {ID: "R-A-003"}, {ID: "R-A-004"},
		{ID: "R-A-005", Manual: true}, {ID: "R-A-006", Manual: true}, {ID: "R-A-007"},
	}
	mapping := map[string][]string{
		"tests/a.TestPass":   {"R-A-001", "R-A-002", "R-A-003"},
		"tests/a.TestFail":   {"R-A-002"},
		"tests/a.TestNotRun": {"R-A-003"},
		"tests/a.TestSkip":   {"R-A-007"},
	}
	results := []Result{
		{ID: "R-A-001", Dir: "/repo/tests/a", Test: "TestPass", Result: "pass"},
		{ID: "R-A-002", Dir: "/repo/tests/a", Test: "TestPass", Result: "pass"},
		{ID: "R-A-003", Dir: "/repo/tests/a", Test: "TestPass", Result: "pass"},
		{ID: "R-A-002", Dir: "/repo/tests/a", Test: "TestFail", Result: "pass"},
		{ID: "R-A-002", Dir: "/repo/tests/a", Test: "TestFail", Result: "fail"},
		{ID: "R-A-007", Dir: "/repo/tests/a", Test: "TestSkip", Result: "skip"},
		{ID: "R-X-001", Dir: "/repo/tests/a", Test: "TestPass", Result: "pass"},
	}
	manual := map[string]Signoff{"R-A-006": {Who: "owner"}}
	cat := Build(root, reqs, mapping, results, manual)
	want := map[string]string{
		"R-A-001": StatusPass, "R-A-002": StatusFail, "R-A-003": StatusNotRun, "R-A-004": StatusUnmapped,
		"R-A-005": StatusManual, "R-A-006": StatusManualSigned, "R-A-007": StatusNotRun,
	}
	for _, r := range cat.Rows {
		if r.Status != want[r.ID] {
			t.Errorf("%s: status %q, want %q", r.ID, r.Status, want[r.ID])
		}
	}
	if !reflect.DeepEqual(cat.Unknown, []string{"R-X-001"}) {
		t.Errorf("unknown = %v", cat.Unknown)
	}
	if n := cat.Count(StatusPass); n != 1 {
		t.Errorf("pass count %d, want 1", n)
	}
}

func TestCheckFloor(t *testing.T) {
	tests := []struct {
		name       string
		floor      string // "" = no file
		passed     int
		wantErr    bool
		wantRaised bool
		wantFile   string
	}{
		{"missing file starts at 0", "", 0, false, false, ""},
		{"equal", "5\n", 5, false, false, "5\n"},
		{"rises", "5\n", 7, false, true, "7\n"},
		{"below fails", "5\n", 4, true, false, "5\n"},
		{"garbage", "five", 5, true, false, "five"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "floor")
			if tt.floor != "" {
				if err := os.WriteFile(path, []byte(tt.floor), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			raised, err := CheckFloor(path, tt.passed)
			if (err != nil) != tt.wantErr || raised != tt.wantRaised {
				t.Fatalf("raised=%v err=%v", raised, err)
			}
			b, _ := os.ReadFile(path)
			if string(b) != tt.wantFile {
				t.Errorf("floor file %q, want %q", b, tt.wantFile)
			}
		})
	}
}
