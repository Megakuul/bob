package jobspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Report is the evaluation report of an evalbuild (or eval) job, `<staging>eval.json`. The
// worker writes it before COMPLETE.json; its SHA-256 is JobResult.EvalSHA, so it is covered
// by the owner's result signature. Everything in it is hostile input to the orchestrator.
type Report struct {
	V         int             `json:"v"` // 1
	Pipelines []Pipeline      `json:"pipelines"`
	Matched   []string        `json:"matched"` // pipeline names the worker ran (sorted)
	Targets   []string        `json:"targets"` // patterns built
	Tests     []string        `json:"tests"`   // patterns tested
	Packages  []PackageReport `json:"packages"`
	Errors    []ErrorReport   `json:"errors"`
}

// PackageReport is one package of the report (GetBuildGraph nodes).
type PackageReport struct {
	Label    string       `json:"label"`
	Platform string       `json:"platform,omitempty"`
	Status   string       `json:"status"` // built | cached | failed | skipped | cancelled | pass | fail
	Deps     []int        `json:"deps,omitempty"`
	Outputs  []string     `json:"outputs,omitempty"`
	Error    *ErrorReport `json:"error,omitempty"`
}

// ErrorReport is one translated error (§02 taxonomy).
type ErrorReport struct {
	Code    string `json:"code"` // BOB-E-NNNN
	Message string `json:"message"`
	Label   string `json:"label,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// Bounds of a report (hostile input, AGENTS.md §5.4).
const (
	MaxReport         = 4 << 20
	MaxReportPackages = 10_000
	MaxReportErrors   = 20
	maxPipelines      = 256
	maxString         = 4 << 10
	maxListed         = 256
)

// ReportKey is the key of the report of a job attempt; staging is JobSpec.Staging + attempt.
func ReportKey(staging string) string { return staging + "eval.json" }

// StagingPrefix is the staging prefix of one attempt: JobSpec.Staging ("staging/<build>/<job>/")
// followed by the attempt (SPEC §05.2.4).
func StagingPrefix(specStaging string, attempt int64) string {
	return fmt.Sprintf("%s%d/", specStaging, attempt)
}

// SHA256 is the hex SHA-256 of b.
func SHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ParseReport decodes and validates a report whose SHA-256 must be sha (JobResult.EvalSHA).
func ParseReport(body []byte, sha string) (*Report, error) {
	if len(body) > MaxReport {
		return nil, fmt.Errorf("evaluation report larger than %d bytes", MaxReport)
	}
	if SHA256(body) != sha {
		return nil, errors.New("evaluation report does not match the signed result")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("evaluation report: %w", err)
	}
	return &r, r.Validate()
}

// Validate checks the bounds and the shape of r.
func (r *Report) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("evaluation report: "+format, args...)
	}
	if r.V != 1 {
		return bad("version %d", r.V)
	}
	if len(r.Pipelines) > maxPipelines {
		return bad("%d pipelines, the limit is %d", len(r.Pipelines), maxPipelines)
	}
	seen := map[string]bool{}
	for _, p := range r.Pipelines {
		if !pipelineName.MatchString(p.Name) || seen[p.Name] {
			return bad("pipeline name %q invalid or repeated", p.Name)
		}
		seen[p.Name] = true
		switch p.Concurrency {
		case "", "queue", "supersede", "parallel":
		default:
			return bad("pipeline %s: concurrency %q", p.Name, p.Concurrency)
		}
		for _, l := range [][]string{p.Triggers, p.Targets, p.Tests, p.Releases} {
			if err := checkStrings(l); err != nil {
				return bad("pipeline %s: %v", p.Name, err)
			}
		}
	}
	for _, l := range [][]string{r.Matched, r.Targets, r.Tests} {
		if err := checkStrings(l); err != nil {
			return bad("%v", err)
		}
	}
	if len(r.Packages) > MaxReportPackages {
		return bad("%d packages, the limit is %d", len(r.Packages), MaxReportPackages)
	}
	for i, p := range r.Packages {
		if err := checkStrings(append([]string{p.Label, p.Platform}, p.Outputs...)); err != nil || p.Label == "" {
			return bad("package %d: label or outputs invalid", i)
		}
		if !slices.Contains([]string{"built", "cached", "failed", "skipped", "cancelled", "pass", "fail"}, p.Status) {
			return bad("package %s: status %q", p.Label, p.Status)
		}
		for _, d := range p.Deps {
			if d < 0 || d >= len(r.Packages) || d == i {
				return bad("package %s: dependency index %d out of range", p.Label, d)
			}
		}
		if p.Error != nil {
			if err := p.Error.check(); err != nil {
				return bad("package %s: %v", p.Label, err)
			}
		}
	}
	if len(r.Errors) > MaxReportErrors {
		return bad("%d errors, the limit is %d", len(r.Errors), MaxReportErrors)
	}
	for _, e := range r.Errors {
		if err := e.check(); err != nil {
			return bad("%v", err)
		}
	}
	return nil
}

func (e *ErrorReport) check() error {
	if !strings.HasPrefix(e.Code, "BOB-") && e.Code != "" {
		return fmt.Errorf("error code %q", e.Code)
	}
	return checkStrings([]string{e.Code, e.Message, e.Label, e.Hint})
}

func checkStrings(l []string) error {
	if len(l) > maxListed+2 {
		return fmt.Errorf("list of %d entries, the limit is %d", len(l), maxListed)
	}
	for _, s := range l {
		if len(s) > maxString || !utf8.ValidString(s) {
			return errors.New("string too long or not UTF-8")
		}
	}
	return nil
}

// Verify is the orchestrator's check of R-PIPE-003: it re-runs Match on the reported pipeline
// definitions with the build's own selection and compares the matched set and the targets with
// what the worker reported. A mismatch is a forged or broken evaluation result.
func (r *Report) Verify(s Selection) error {
	want, err := Match(r.Pipelines, s)
	if err != nil {
		return err
	}
	got := slices.Sorted(slices.Values(r.Matched))
	if !slices.Equal(want, slices.Compact(got)) || len(got) != len(r.Matched) {
		return ErrMatchMismatch
	}
	if !s.RunsPipelines() {
		return nil
	}
	targets, tests := Targets(r.Pipelines, want)
	if !sameSet(targets, r.Targets) || !sameSet(tests, r.Tests) {
		return ErrMatchMismatch
	}
	return nil
}

// ErrMatchMismatch is the rejection of R-PIPE-003.
var ErrMatchMismatch = errors.New("evaluation result rejected: pipeline match mismatch")

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}
