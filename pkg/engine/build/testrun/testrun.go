// Package testrun is the engine side of `bob test` (SPEC §03.0.5, R-FW-024, R-FW-064, O-02-9):
// it finds the test units among the planned derivations, decides which results are cached,
// makes rerun derivations and reads the results the builder's test-run kind wrote.
//
// A test unit is a derivation of bob-builder kind "test-run"; every ecosystem's test unit ends
// in exactly one. Its result is cached exactly when its output is valid in the store (or
// substitutable): a failing test fails its derivation, so only passing results exist.
package testrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine/drv"
)

// Kind is the bob-builder job kind of a test unit's run derivation.
const Kind = "test-run"

// RerunAttribute is the structured attribute that holds the nonce of a rerun derivation.
const RerunAttribute = "bobRerun"

// maxResult bounds test-result.json: cases carry at most 64 KiB of output each, and a unit
// with thousands of cases stays far below this.
const maxResult = 64 << 20

// Status is the outcome of one test unit, as in the test.result event.
type Status string

const (
	StatusPass    Status = "pass"    // built (run) now and passed
	StatusFail    Status = "fail"    // built now and failed (never cached)
	StatusCached  Status = "cached"  // the passing result was already in the store or a cache
	StatusSkipped Status = "skipped" // cannot run on this machine (R-PLAT, --require-all-tests off)
)

// Unit is one test unit.
type Unit struct {
	Label   string // label of the unit, e.g. //pkg/foo:go_test
	Drv     *drv.Derivation
	DrvPath string
	Out     string // store path of output "out" (holds test-result.json and test.log)
}

// Units returns the test units among drvs (finalised derivations of kind "test-run"),
// sorted by label. Two units with one label are an error: the label names the result.
func Units(drvs []*drv.Derivation) ([]Unit, error) {
	var units []Unit
	seen := map[string]bool{}
	for _, d := range drvs {
		label, ok := unitLabel(d)
		if !ok {
			continue
		}
		p, err := d.Path()
		if err != nil {
			return nil, fmt.Errorf("testrun: %w", err)
		}
		out := ""
		for _, o := range d.Outputs {
			if o.Name == "out" {
				out = o.Path
			}
		}
		if out == "" {
			return nil, fmt.Errorf("testrun: test unit %s has no output out", label)
		}
		if seen[label] {
			return nil, fmt.Errorf("testrun: two test units are labelled %s", label)
		}
		seen[label] = true
		units = append(units, Unit{Label: label, Drv: d, DrvPath: p, Out: out})
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Label < units[j].Label })
	return units, nil
}

// unitLabel returns the job label of a test-run derivation.
func unitLabel(d *drv.Derivation) (string, bool) {
	spec, _ := d.Structured["bobSpec"].(map[string]any)
	if kind, _ := spec["kind"].(string); kind != Kind {
		return "", false
	}
	label, _ := spec["label"].(string)
	return label, label != ""
}

// Validity is the store query Cached needs; *store.Store implements it.
type Validity interface {
	Invalid(ctx context.Context, paths []string) ([]string, error)
}

// Cached returns the labels of the units whose passing result is already valid in the store,
// in one batched query. The caller substitutes from bob's caches first (a substituted result
// counts as cached too) and builds only the others.
func Cached(ctx context.Context, v Validity, units []Unit) (map[string]bool, error) {
	if len(units) == 0 {
		return map[string]bool{}, nil
	}
	paths := make([]string, len(units))
	for i, u := range units {
		paths[i] = u.Out
	}
	invalid, err := v.Invalid(ctx, paths)
	if err != nil {
		return nil, fmt.Errorf("testrun: %w", err)
	}
	missing := map[string]bool{}
	for _, p := range invalid {
		missing[p] = true
	}
	cached := map[string]bool{}
	for _, u := range units {
		if !missing[u.Out] {
			cached[u.Label] = true
		}
	}
	return cached, nil
}

// Rerun returns a copy of the unit's derivation that differs only by the attribute
// bobRerun=nonce, so it has new output paths and runs the test again (bob test --rerun).
// Nix's --rebuild would compare the new output with the cached one and fail on every test,
// because test-result.json and test.log hold timings (O-02-9, measured in this package's
// tests). Test-run derivations are leaves, so no other derivation changes. The cached result
// of the original derivation stays valid.
func Rerun(u Unit, nonce string, r drv.Resolver) (Unit, error) {
	if nonce == "" {
		return Unit{}, errors.New("testrun: empty rerun nonce")
	}
	d := *u.Drv
	d.Structured = make(map[string]any, len(u.Drv.Structured)+1)
	for k, v := range u.Drv.Structured {
		d.Structured[k] = v
	}
	d.Structured[RerunAttribute] = nonce
	d.Outputs = append([]drv.Output(nil), u.Drv.Outputs...)
	d.Env = make(map[string]string, len(u.Drv.Env))
	for k, v := range u.Drv.Env {
		d.Env[k] = v
	}
	if err := d.Finalize(r); err != nil {
		return Unit{}, fmt.Errorf("testrun: rerun %s: %w", u.Label, err)
	}
	out, err := Units([]*drv.Derivation{&d})
	if err != nil || len(out) != 1 {
		return Unit{}, fmt.Errorf("testrun: rerun %s: %v", u.Label, err)
	}
	return out[0], nil
}

// Result is test-result.json (schema version 1, SPEC §03.0.5).
type Result struct {
	Version  int     `json:"version"`
	Label    string  `json:"label"`
	Passed   bool    `json:"passed"`
	Duration float64 `json:"durationSeconds"`
	Cases    []Case  `json:"cases"`
}

// Case is one test case of a Result.
type Case struct {
	Name     string  `json:"name"`
	Result   string  `json:"result"` // pass | fail | skip
	Duration float64 `json:"durationSeconds"`
	Output   string  `json:"output,omitempty"`
}

// DurationMs is the run time in whole milliseconds (the durationMs of the test.result event).
func (r Result) DurationMs() int64 { return int64(r.Duration*1000 + 0.5) }

// Failed lists the names of the failing cases (the failed[] of the test.result event).
func (r Result) Failed() []string {
	var out []string
	for _, c := range r.Cases {
		if c.Result == "fail" {
			out = append(out, c.Name)
		}
	}
	return out
}

// ReadResult reads <dir>/test-result.json of a unit's output; dir is the output as the host
// sees it (below the bob home on hosts without /nix).
func ReadResult(dir string) (Result, error) {
	f, err := os.Open(filepath.Join(dir, "test-result.json"))
	if err != nil {
		return Result{}, fmt.Errorf("testrun: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxResult+1))
	if err != nil {
		return Result{}, fmt.Errorf("testrun: %w", err)
	}
	if len(b) > maxResult {
		return Result{}, fmt.Errorf("testrun: %s/test-result.json is larger than %d bytes", dir, maxResult)
	}
	return decodeResult(b)
}

func decodeResult(b []byte) (Result, error) {
	var r Result
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return Result{}, fmt.Errorf("testrun: test result: %w", err)
	}
	if r.Version != 1 || r.Label == "" || r.Duration < 0 {
		return Result{}, fmt.Errorf("testrun: test result is not a valid version 1 result")
	}
	for _, c := range r.Cases {
		switch c.Result {
		case "pass", "fail", "skip":
		default:
			return Result{}, fmt.Errorf("testrun: test case %q has result %q", c.Name, c.Result)
		}
	}
	return r, nil
}

// FailedResult finds the result the builder wrote to the build log of a failed test
// (`bob:: {"type":"test-result","result":{…}}`); ok is false when the test never ran (for
// example a service did not become ready).
func FailedResult(log io.Reader) (Result, bool) {
	sc := bufio.NewScanner(log)
	sc.Buffer(make([]byte, 64<<10), maxResult)
	var last Result
	found := false
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "bob:: ")
		if !ok || !strings.HasPrefix(rest, `{"type":"test-result"`) {
			continue
		}
		var ev struct {
			Type   string          `json:"type"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(rest), &ev) != nil {
			continue
		}
		if r, err := decodeResult(ev.Result); err == nil {
			last, found = r, true
		}
	}
	return last, found
}
