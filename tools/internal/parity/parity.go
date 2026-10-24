// Package parity builds bob's parity catalogue (HARNESS.md §11): the list of
// SPEC requirements, the tests that cover each one, their status, and the
// pass-count floor.
package parity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Paths relative to the repository root.
const (
	SpecPath    = "SPEC.md"
	DocPath     = "docs/parity.md"
	FloorPath   = "tests/parity/floor"
	ManualPath  = "tests/parity/manual.json"
	ResultsPath = "out/parity/results.jsonl"
	// CoversPkg is the import path of the runtime API tests call.
	CoversPkg = "github.com/megakuul/bob/tests/parity"
)

// Status values, as printed in docs/parity.md.
const (
	StatusPass         = "pass"
	StatusFail         = "fail"
	StatusNotRun       = "not run"
	StatusUnmapped     = "unmapped"
	StatusManual       = "manual"
	StatusManualSigned = "manual-signed"
)

// Requirement is one SPEC entry. A heading with two ids yields two entries.
type Requirement struct {
	ID      string
	Area    string
	Title   string
	Section string
	Manual  bool
}

var (
	reqHeading = regexp.MustCompile(`^#### (R-[A-Z]+-[0-9]{3}[a-z]?)(?: / (R-[A-Z]+-[0-9]{3}[a-z]?))? (.+)$`)
	idPattern  = regexp.MustCompile(`^R-([A-Z]+)-[0-9]{3}[a-z]?$`)
)

// ParseSpec extracts the requirements from SPEC.md, in SPEC order.
func ParseSpec(r io.Reader) ([]Requirement, error) {
	var (
		reqs    []Requirement
		section string
		cur     []int // indexes of the entries under the current heading
		seen    = map[string]bool{}
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### "):
			section = strings.TrimSpace(strings.TrimLeft(line, "#"))
			cur = nil
		case strings.HasPrefix(line, "#### "):
			cur = nil
			m := reqHeading.FindStringSubmatch(line)
			if m == nil {
				if strings.HasPrefix(line, "#### R-") {
					return nil, fmt.Errorf("spec: malformed requirement heading %q", line)
				}
				continue
			}
			for _, id := range m[1:3] {
				if id == "" {
					continue
				}
				if seen[id] {
					return nil, fmt.Errorf("spec: duplicate requirement %s", id)
				}
				seen[id] = true
				cur = append(cur, len(reqs))
				reqs = append(reqs, Requirement{ID: id, Area: area(id), Title: m[3], Section: section})
			}
		default:
			// "- **Acceptance:** (manual) …" and "- **Acceptance (manual):** …"
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "- **Acceptance (manual):**") ||
				strings.HasPrefix(t, "- **Acceptance:** (manual)") {
				for _, i := range cur {
					reqs[i].Manual = true
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	return reqs, nil
}

func area(id string) string {
	if m := idPattern.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return ""
}

// Result is one line of the runtime results file (see tests/parity).
type Result struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"`
	Test   string `json:"test"`
	Result string `json:"result"`
}

// ReadResults reads the results file. A missing file means no test ran.
func ReadResults(path string) ([]Result, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parity results: %w", err)
	}
	defer f.Close()
	var out []Result
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("parity results %s:%d: %w", path, n, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("parity results: %w", err)
	}
	return out, nil
}

// Signoff is a manual requirement's sign-off in tests/parity/manual.json,
// keyed by requirement id.
type Signoff struct {
	Date     string `json:"date"`
	Who      string `json:"who"`
	Evidence string `json:"evidence"`
}

// ReadManual reads the sign-offs. A missing file means none.
func ReadManual(path string) (map[string]Signoff, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Signoff{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parity manual: %w", err)
	}
	m := map[string]Signoff{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parity manual %s: %w", path, err)
	}
	return m, nil
}

// Row is one requirement of the catalogue.
type Row struct {
	Requirement
	Tests  []string // "<dir>.<Test>", sorted
	Status string
}

// Catalogue is the parity catalogue.
type Catalogue struct {
	Rows []Row
	// Unknown lists ids that tests cover but SPEC does not define.
	Unknown []string
}

// Build combines the requirement list, the static mapping (test key → ids),
// the runtime results and the manual sign-offs. root relativises the
// absolute directories in results.
func Build(root string, reqs []Requirement, mapping map[string][]string, results []Result, manual map[string]Signoff) Catalogue {
	tests := map[string]map[string]bool{} // id → test keys
	add := func(id, key string) {
		if tests[id] == nil {
			tests[id] = map[string]bool{}
		}
		tests[id][key] = true
	}
	for key, ids := range mapping {
		for _, id := range ids {
			add(id, key)
		}
	}
	// outcome per (id, test): fail beats skip beats pass, so one failing
	// subtest fails the test.
	rank := map[string]int{"pass": 1, "skip": 2, "fail": 3}
	outcome := map[[2]string]string{}
	for _, r := range results {
		key := testKey(root, r.Dir, r.Test)
		add(r.ID, key)
		k := [2]string{r.ID, key}
		if rank[r.Result] > rank[outcome[k]] {
			outcome[k] = r.Result
		}
	}

	var c Catalogue
	known := map[string]bool{}
	for _, req := range reqs {
		known[req.ID] = true
		row := Row{Requirement: req}
		for key := range tests[req.ID] {
			row.Tests = append(row.Tests, key)
		}
		sort.Strings(row.Tests)
		row.Status = status(row, outcome, manual)
		c.Rows = append(c.Rows, row)
	}
	for id := range tests {
		if !known[id] {
			c.Unknown = append(c.Unknown, id)
		}
	}
	sort.Strings(c.Unknown)
	return c
}

func status(row Row, outcome map[[2]string]string, manual map[string]Signoff) string {
	if row.Manual {
		if _, ok := manual[row.ID]; ok {
			return StatusManualSigned
		}
		return StatusManual
	}
	if len(row.Tests) == 0 {
		return StatusUnmapped
	}
	st := StatusPass
	for _, t := range row.Tests {
		switch outcome[[2]string{row.ID, t}] {
		case "fail":
			return StatusFail
		case "pass":
		default: // skipped or never ran
			st = StatusNotRun
		}
	}
	return st
}

func testKey(root, dir, test string) string {
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		dir = rel
	}
	return filepath.ToSlash(dir) + "." + test
}

// Count returns how many rows have the given status.
func (c Catalogue) Count(status string) int {
	n := 0
	for _, r := range c.Rows {
		if r.Status == status {
			n++
		}
	}
	return n
}

// Markdown renders docs/parity.md.
func (c Catalogue) Markdown() string {
	var b strings.Builder
	b.WriteString("# Parity catalogue\n\n")
	b.WriteString("Generated by `go run ./tools/ci -only parity` from SPEC.md and the tests' `parity.Covers` calls (HARNESS.md §11). Do not edit.\n\n")

	statuses := []string{StatusPass, StatusFail, StatusNotRun, StatusUnmapped, StatusManual, StatusManualSigned}
	type tally map[string]int
	byArea := map[string]tally{}
	var areas []string
	for _, r := range c.Rows {
		if byArea[r.Area] == nil {
			byArea[r.Area] = tally{}
			areas = append(areas, r.Area)
		}
		byArea[r.Area][r.Status]++
	}
	sort.Strings(areas)
	b.WriteString("## Totals\n\n| Area | Total | " + strings.Join(statuses, " | ") + " |\n|---|---|")
	b.WriteString(strings.Repeat("---|", len(statuses)) + "\n")
	row := func(name string, total int, t tally) {
		b.WriteString("| " + name + " | " + strconv.Itoa(total))
		for _, s := range statuses {
			b.WriteString(" | " + strconv.Itoa(t[s]))
		}
		b.WriteString(" |\n")
	}
	all := tally{}
	for _, a := range areas {
		total := 0
		for s, n := range byArea[a] {
			total += n
			all[s] += n
		}
		row(a, total, byArea[a])
	}
	row("**all**", len(c.Rows), all)

	b.WriteString("\n## Requirements\n\n| Id | Title | SPEC section | Tests | Status |\n|---|---|---|---|---|\n")
	for _, r := range c.Rows {
		tests := "—"
		if len(r.Tests) > 0 {
			tests = "`" + strings.Join(r.Tests, "`<br>`") + "`"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.ID, cell(r.Title), cell(r.Section), tests, r.Status)
	}
	if len(c.Unknown) > 0 {
		b.WriteString("\n## Ids covered by tests but not in SPEC.md\n\n")
		for _, id := range c.Unknown {
			b.WriteString("- " + id + "\n")
		}
	}
	return b.String()
}

func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// ReadFloor reads the pass-count floor. A missing file means 0.
func ReadFloor(path string) (int, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("parity floor: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("parity floor %s: want one non-negative integer, got %q", path, strings.TrimSpace(string(b)))
	}
	return n, nil
}

// CheckFloor compares the pass count with the floor in path. A lower count
// is an error; a higher one is written as the new floor, so the floor only
// rises (lowering it is a protected change, HARNESS.md §2.1). It reports
// whether the file changed.
func CheckFloor(path string, passed int) (raised bool, err error) {
	floor, err := ReadFloor(path)
	if err != nil {
		return false, err
	}
	if passed < floor {
		return false, fmt.Errorf("parity: %d requirements pass, below the floor of %d (%s)", passed, floor, path)
	}
	if passed == floor {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(passed)+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("parity floor: %w", err)
	}
	return true, nil
}

// Generate builds the catalogue for the repository at root from SPEC.md,
// the static scan, the results file and the manual sign-offs.
func Generate(ctx context.Context, root string) (Catalogue, error) {
	f, err := os.Open(filepath.Join(root, SpecPath))
	if err != nil {
		return Catalogue{}, fmt.Errorf("parity: %w", err)
	}
	defer f.Close()
	reqs, err := ParseSpec(f)
	if err != nil {
		return Catalogue{}, err
	}
	mapping, err := Scan(ctx, root)
	if err != nil {
		return Catalogue{}, err
	}
	results, err := ReadResults(filepath.Join(root, ResultsPath))
	if err != nil {
		return Catalogue{}, err
	}
	manual, err := ReadManual(filepath.Join(root, ManualPath))
	if err != nil {
		return Catalogue{}, err
	}
	return Build(root, reqs, mapping, results, manual), nil
}
