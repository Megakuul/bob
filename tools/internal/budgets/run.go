package budgets

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Regression is how much worse than its baseline a row may get (HARNESS.md §12.2).
const Regression = 0.25

// Result is the outcome of one row.
type Result struct {
	Row      Row
	Measured bool    // false: no benchmark yet
	Value    float64 // statistic in the row's unit
	Limit    float64
	Baseline float64 // 0 when none
	Err      error   // non-nil: the row failed
}

// Status is the one-word state of a result.
func (r Result) Status() string {
	switch {
	case !r.Measured:
		return "no benchmark yet"
	case r.Err != nil:
		return "FAIL"
	}
	return "ok"
}

// Options select the rows a run measures.
type Options struct {
	Root    string
	Profile string // Profile8c or Profile2c
	Slow    bool   // false: only suite "fast"
	// Exec runs one bench command; nil runs it with os/exec from Root.
	Exec func(ctx context.Context, root, command string) ([]byte, error)
}

// Run measures the selected rows of f. It returns every row's result and an
// error when at least one measured row failed.
func Run(ctx context.Context, f *File, o Options) ([]Result, error) {
	if o.Exec == nil {
		o.Exec = execCommand
	}
	if o.Profile == "" {
		o.Profile = Profile8c
	}
	base, err := loadBaseline(o.Root, o.Profile)
	if err != nil {
		return nil, err
	}
	busy := loadAvg() > 1.0

	// Rows that share a command are measured by one invocation.
	var results []Result
	values := map[string]float64{}
	ran := map[string]error{}
	measure := func(cmd string) error {
		if err, ok := ran[cmd]; ok {
			return err
		}
		out, err := o.Exec(ctx, o.Root, cmd)
		if err == nil {
			err = parseOutput(out, values)
		}
		ran[cmd] = err
		return err
	}
	for _, r := range f.Rows {
		if r.Suite != "fast" && !o.Slow {
			continue
		}
		limit, ok := r.Limit[o.Profile]
		if !ok {
			continue // budget not defined for this profile
		}
		res := Result{Row: r, Limit: limit, Baseline: base[r.ID]}
		if r.Bench == "" {
			results = append(results, res)
			continue
		}
		res.Measured = true
		res.Err = check(f, &res, measure, values)
		if res.Err != nil && busy && res.Row.Class() != "size" && res.Row.Class() != "count" {
			// A loaded host gets one rerun before a timing failure counts.
			delete(ran, r.Bench)
			res.Err = check(f, &res, measure, values)
		}
		if res.Err == nil && (res.Baseline == 0 || res.Value < res.Baseline) {
			base[r.ID] = res.Value
		}
		results = append(results, res)
	}
	if err := saveBaseline(o.Root, o.Profile, base); err != nil {
		return results, err
	}
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, r.Err.Error())
		}
	}
	if len(failed) > 0 {
		return results, fmt.Errorf("%d budget rows failed:\n%s", len(failed), strings.Join(failed, "\n"))
	}
	return results, nil
}

func check(f *File, res *Result, measure func(string) error, values map[string]float64) error {
	r := res.Row
	if err := measure(r.Bench); err != nil {
		return fmt.Errorf("%s %s: bench %q: %w", r.ID, r.What, r.Bench, err)
	}
	v, ok := values[r.ID]
	if !ok {
		return fmt.Errorf("%s %s: bench %q printed no BUDGET line for it", r.ID, r.What, r.Bench)
	}
	res.Value = v
	if max := res.Limit * (1 + f.Tolerance.Tol(r)); v > max {
		return fmt.Errorf("%s %s: %s %g %s > limit %g %s (+%g%% tolerance)",
			r.ID, r.What, r.Stat, v, r.Unit, res.Limit, r.Unit, f.Tolerance.Tol(r)*100)
	}
	if res.Baseline > 0 && v > res.Baseline*(1+Regression) {
		return fmt.Errorf("%s %s: %g %s is more than %g%% worse than baseline %g",
			r.ID, r.What, v, r.Unit, Regression*100, res.Baseline)
	}
	return nil
}

// parseOutput reads "BUDGET <id> <value>" lines; other lines are ignored.
func parseOutput(out []byte, values map[string]float64) error {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || f[0] != "BUDGET" {
			continue
		}
		v, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			return fmt.Errorf("bad BUDGET line %q: %w", sc.Text(), err)
		}
		values[f[1]] = v
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read bench output: %w", err)
	}
	return nil
}

func execCommand(ctx context.Context, root, command string) ([]byte, error) {
	args := strings.Fields(command)
	if len(args) == 0 {
		return nil, errors.New("empty bench command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func baselinePath(root, profile string) string {
	return filepath.Join(root, "tests/budgets", "baseline-"+profile+".json")
}

func loadBaseline(root, profile string) (map[string]float64, error) {
	m := map[string]float64{}
	data, err := os.ReadFile(baselinePath(root, profile))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse baseline %s: %w", baselinePath(root, profile), err)
	}
	return m, nil
}

// saveBaseline records improvements. It writes nothing until a row has been
// measured, so runs without benchmarks leave the tree clean.
func saveBaseline(root, profile string, m map[string]float64) error {
	if len(m) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, id := range ids {
		fmt.Fprintf(&b, "  %q: %s", id, strconv.FormatFloat(m[id], 'g', -1, 64))
		if i < len(ids)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if err := os.WriteFile(baselinePath(root, profile), b.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	return nil
}

// loadAvg is the 1-minute load average, or 0 when it cannot be read.
func loadAvg() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
