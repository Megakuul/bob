// Package budgets generates the harness budgets file (HARNESS.md §12.1) from the
// SPEC Appendix B budget table and checks measurements against it (§12.2).
package budgets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// FilePath is the budgets file, relative to the repository root.
const FilePath = "tests/budgets/budgets.json"

// Profiles, in the order the SPEC tables list them.
const (
	Profile8c = "8c15g"
	Profile2c = "2c4g"
)

// Note is the header of the budgets file. It states the rule for rows that have
// no benchmark, so a reader of the file does not need to find it in the code.
const Note = "GENERATED from SPEC.md Appendix B by tools/internal/budgets; do not edit. " +
	"All 97 rows are [proposal] budgets (finalised in M1.9). A row fails when its statistic is " +
	"greater than limit x (1 + tolerance[class]). A row whose \"bench\" is empty has no benchmark " +
	"yet: the runner reports it as \"no benchmark yet\" and does not fail. Once the feature exists, " +
	"its task registers a benchmark command (tools/internal/budgets/benches.go) and the row counts."

// Tolerance is the allowed overshoot per tolerance class (HARNESS.md §12.2).
type Tolerance struct {
	Time       float64 `json:"time"`
	LatencyP99 float64 `json:"latency_p99"`
	Size       float64 `json:"size"`
	Count      float64 `json:"count"`
}

// DefaultTolerance is the protected tolerance of HARNESS.md §12.2.
var DefaultTolerance = Tolerance{Time: 0.10, LatencyP99: 0.15, Size: 0, Count: 0}

// Row is one budget.
type Row struct {
	ID      string             `json:"id"`
	Section string             `json:"section"`
	Req     string             `json:"req"`
	What    string             `json:"what"`
	Budget  string             `json:"budget"` // SPEC text, verbatim
	Metric  string             `json:"metric"`
	Stat    string             `json:"stat"`
	Runs    int                `json:"runs"`
	Value   float64            `json:"value"` // first parsed limit, in Unit
	Unit    string             `json:"unit"`
	Limit   map[string]float64 `json:"limit"` // per profile, in Unit
	Profile string             `json:"profile"`
	Source  string             `json:"source"`
	Status  string             `json:"status"`
	Suite   string             `json:"suite"` // fast | slow
	Bench   string             `json:"bench"` // command; empty = no benchmark yet
	Busy    bool               `json:"busy"`
}

// Class returns the tolerance class of the row.
func (r Row) Class() string {
	switch {
	case r.Unit == "MiB" || r.Unit == "MB" || r.Unit == "KiB" || r.Unit == "GiB" || r.Unit == "%":
		return "size"
	case r.Unit == "count" || r.Unit == "req/s":
		return "count"
	case r.Stat == "p95" || r.Stat == "p99":
		return "latency_p99"
	}
	return "time"
}

// Tol returns the tolerance that applies to the row.
func (t Tolerance) Tol(r Row) float64 {
	switch r.Class() {
	case "size":
		return t.Size
	case "count":
		return t.Count
	case "latency_p99":
		return t.LatencyP99
	}
	return t.Time
}

// File is the budgets file.
type File struct {
	V         int       `json:"v"`
	Note      string    `json:"note"`
	Tolerance Tolerance `json:"tolerance"`
	Rows      []Row     `json:"rows"`
}

var (
	reqRe   = regexp.MustCompile(`R-[A-Z]+-\d{3}`)
	limitRe = regexp.MustCompile(`≤\s*(\d+(?:\.\d+)?)\s*(MiB/s|req/s|ms|s\b|MiB|MB|KiB|GiB|×|%)?(\s*\[(?:8c/15G|2c/4G)\])?`)
	numRe   = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(ms|s\b|MiB|MB|KiB|GiB)`)
)

// Parse extracts the Appendix B rows from SPEC.md. A row it cannot parse is an
// error, so the budgets file cannot drift silently from the SPEC.
func Parse(spec []byte) ([]Row, error) {
	start := bytes.Index(spec, []byte("\n# Appendix B"))
	if start < 0 {
		return nil, errors.New("SPEC.md: no \"# Appendix B\" heading")
	}
	body := spec[start:]
	if end := bytes.Index(body, []byte("<!-- SECTIONS -->")); end >= 0 {
		body = body[:end]
	}
	var rows []Row
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "| §") {
			continue
		}
		cells := splitCells(line)
		if len(cells) != 5 {
			return nil, fmt.Errorf("Appendix B row %d: %d cells, want 5: %q", len(rows)+1, len(cells), line)
		}
		r, err := parseRow(fmt.Sprintf("B-%03d", len(rows)+1), cells)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// splitCells splits a Markdown table row, keeping "|" inside backticks.
func splitCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var cells []string
	var cur strings.Builder
	code := false
	for _, c := range line {
		switch {
		case c == '`':
			code = !code
		case c == '|' && !code:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func parseRow(id string, c []string) (Row, error) {
	r := Row{ID: id, Section: c[0], What: c[1], Budget: c[2], Profile: c[3], Source: c[4],
		Status: "proposal", Runs: 5}
	if m := reqRe.FindString(c[0] + " " + c[1] + " " + c[2]); m != "" {
		r.Req = m
	}
	budget := strings.ReplaceAll(c[2], "**", "")
	type lim struct {
		v       float64
		unit    string
		profile string
	}
	var lims []lim
	for _, m := range limitRe.FindAllStringSubmatch(budget, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return r, fmt.Errorf("%s: %w", id, err)
		}
		l := lim{v: v, unit: strings.TrimSpace(m[2])}
		switch {
		case strings.Contains(m[3], "8c"):
			l.profile = Profile8c
		case strings.Contains(m[3], "2c"):
			l.profile = Profile2c
		}
		lims = append(lims, l)
	}
	if len(lims) == 0 {
		// Budgets written without "≤" (sandbox defaults, "flush_interval + 10 s").
		for _, m := range numRe.FindAllStringSubmatch(budget, 1) {
			v, _ := strconv.ParseFloat(m[1], 64)
			lims = append(lims, lim{v: v, unit: m[2]})
		}
	}
	if len(lims) == 0 {
		return r, fmt.Errorf("%s (%s): no limit in budget %q", id, r.What, c[2])
	}
	// "≤ 5 / ≤ 2 s": a bare number takes the unit of the next one that has one.
	for i := len(lims) - 1; i >= 0; i-- {
		if lims[i].unit == "" && i+1 < len(lims) {
			lims[i].unit = lims[i+1].unit
		}
	}
	for i := range lims {
		if lims[i].unit == "" {
			lims[i].unit = "count"
		}
	}
	r.Value, r.Unit = lims[0].v, lims[0].unit
	r.Limit = map[string]float64{}
	// "≤ a / ≤ b" (or "≤ a / ≤ b / ≤ c / ≤ d") under "[8c/15G] / [2c/4G]": the
	// first half belongs to [8c/15G], the second half to [2c/4G].
	rest := strings.Trim(limitRe.ReplaceAllString(budget, ""), " /")
	twoProfiles := strings.Contains(c[3], "[8c/15G] / [2c/4G]") && rest == "" && len(lims)%2 == 0
	for i, l := range lims {
		if l.unit != r.Unit {
			continue // a second measure in the same row (e.g. "IR 64 MiB")
		}
		switch {
		case l.profile != "":
			if _, ok := r.Limit[l.profile]; !ok {
				r.Limit[l.profile] = l.v
			}
		case twoProfiles && len(lims) >= 2 && i == 0:
			r.Limit[Profile8c] = l.v
		case twoProfiles && len(lims) >= 2 && i == len(lims)/2:
			r.Limit[Profile2c] = l.v
		}
	}
	if len(r.Limit) == 0 {
		switch {
		case strings.HasPrefix(c[3], "[8c/15G]") && !strings.Contains(c[3], "2c"):
			r.Limit[Profile8c] = r.Value
		case strings.HasPrefix(c[3], "[2c/4G]") && !strings.Contains(c[3], "8c"):
			r.Limit[Profile2c] = r.Value
		default: // "both", "—", "as stated", "local", "[8c/15G], [2c/4G]"
			r.Limit[Profile8c], r.Limit[Profile2c] = r.Value, r.Value
		}
	}
	r.Stat, r.Metric = statMetric(r, budget, limitRe.FindStringIndex(budget))
	r.Suite = suite(r)
	r.Busy = strings.Contains(c[4], "busy") || strings.Contains(c[4], "loaded")
	r.Bench = Benches[r.ID]
	return r, nil
}

func statMetric(r Row, budget string, first []int) (stat, metric string) {
	lb := strings.ToLower(budget + " " + r.What)
	// The statistic named right before the first limit wins ("p50 ≤ 5 ms, p99 ≤ 20 ms").
	pre := ""
	if first != nil {
		pre = budget[:first[0]]
	}
	switch {
	case strings.Contains(pre, "p50"):
		stat = "median"
	case strings.Contains(pre, "p99"):
		stat = "p99"
	case strings.Contains(pre, "p95"):
		stat = "p95"
	case strings.Contains(lb, "p99"):
		stat = "p99"
	case strings.Contains(lb, "p95"):
		stat = "p95"
	case strings.Contains(lb, "rss"):
		stat = "max"
	case r.Unit == "MiB" || r.Unit == "MB" || r.Unit == "KiB" || r.Unit == "GiB" ||
		r.Unit == "count" || r.Unit == "req/s" || r.Unit == "%":
		stat = "value"
	default:
		stat = "median"
	}
	switch r.Unit {
	case "s":
		metric = "wall_s"
	case "ms":
		metric = "wall_ms"
	case "×":
		metric = "ratio"
	case "MiB", "MB", "KiB", "GiB":
		metric = "size_" + strings.ToLower(r.Unit)
		if strings.Contains(lb, "rss") {
			metric = "rss_" + strings.ToLower(r.Unit)
		}
	case "req/s":
		metric = "req_per_s"
	case "%":
		metric = "percent"
	default:
		metric = "count"
	}
	return stat, metric
}

// Slow keywords win over fast ones: cold builds, big graphs, 100 MiB transfers,
// chaos and fleet latencies, Vite/HMR and tsserver (HARNESS.md §12.1).
var (
	slowWords = []string{"cold", "10,000", "100 mib", "vite", "hmr", "tsserver", "dead worker",
		"polling", "100 workers", "100m lines", "split batch", "10 × 1 gib", "fresh machine",
		"substitution", "webhook", "checkout", "revocation", "registration", "enqueue",
		"live log", "live line", "degraded", "chain", "200-pkg", "100-package"}
	fastWords = []string{"no-op", "no change", "nothing changed", "namespace entry", "shim start",
		"hook fast path", "`bob status`", "narinfo", "requests per job", "idle", "closure (nar)",
		"binary", "sizes", "first load", "inputs unchanged", "warm definition", "label completion",
		"evaluator sandbox", "unary"}
)

func suite(r Row) string {
	s := strings.ToLower(r.What + " " + r.Budget)
	for _, w := range slowWords {
		if strings.Contains(s, w) {
			return "slow"
		}
	}
	for _, w := range fastWords {
		if strings.Contains(s, w) {
			return "fast"
		}
	}
	return "slow"
}

// Generate builds the budgets file from SPEC.md under root.
func Generate(root string) (*File, error) {
	spec, err := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if err != nil {
		return nil, fmt.Errorf("read SPEC: %w", err)
	}
	rows, err := Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("parse SPEC Appendix B: %w", err)
	}
	return &File{V: 1, Note: Note, Tolerance: DefaultTolerance, Rows: rows}, nil
}

// Encode writes the file with one row per line, so diffs show changed rows.
func (f *File) Encode() ([]byte, error) {
	var b bytes.Buffer
	head := func(k string, v any) error {
		j, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode %s: %w", k, err)
		}
		fmt.Fprintf(&b, "%q:%s,\n ", k, j)
		return nil
	}
	b.WriteString("{")
	for _, kv := range []struct {
		k string
		v any
	}{{"v", f.V}, {"note", f.Note}, {"tolerance", f.Tolerance}} {
		if err := head(kv.k, kv.v); err != nil {
			return nil, err
		}
	}
	b.WriteString("\"rows\":[\n")
	for i, r := range f.Rows {
		j, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", r.ID, err)
		}
		b.WriteString("  ")
		b.Write(j)
		if i < len(f.Rows)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(" ]}\n")
	return b.Bytes(), nil
}

// Write regenerates the budgets file under root (used by the gen stage).
func Write(root string) error {
	f, err := Generate(root)
	if err != nil {
		return err
	}
	data, err := f.Encode()
	if err != nil {
		return err
	}
	p := filepath.Join(root, FilePath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("budgets dir: %w", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return fmt.Errorf("write budgets file: %w", err)
	}
	return nil
}

// Check fails when the committed budgets file differs from what SPEC.md generates.
func Check(root string) (*File, error) {
	f, err := Generate(root)
	if err != nil {
		return nil, err
	}
	want, err := f.Encode()
	if err != nil {
		return nil, err
	}
	got, err := os.ReadFile(filepath.Join(root, FilePath))
	if err != nil {
		return nil, fmt.Errorf("read budgets file: %w", err)
	}
	if !bytes.Equal(got, want) {
		return nil, fmt.Errorf("%s is stale against SPEC.md Appendix B; regenerate it (gen stage)", FilePath)
	}
	return f, nil
}
