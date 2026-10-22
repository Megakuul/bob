package translate

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// A capture is a recorded Nix run (HARNESS.md §5.3 replay mode, S15 `capture`): meta.json
// holds bob's context and the outcome, stream.log (or stream.log.gz) Nix's stderr. Replaying a
// capture feeds the translator exactly the recorded stream.

// maxMetaBytes bounds meta.json (it holds one run's context).
const maxMetaBytes = 64 << 20

// Capture is meta.json of a recorded run.
type Capture struct {
	Name     string          `json:"name"`
	Nix      string          `json:"nix"`
	Command  []string        `json:"command,omitempty"`
	Outcome  Outcome         `json:"outcome"`
	Context  *Context        `json:"context,omitempty"`
	Precheck []CapturedError `json:"precheck,omitempty"` // errors of bob's Go pre-checks (Nix never ran)
	Stdout   []string        `json:"stdout,omitempty"`
	Note     string          `json:"note,omitempty"`

	dir string
}

// CapturedError is a recorded bob error (the --json error object of the capture tool).
type CapturedError struct {
	Code      string            `json:"code"` // "BOB-E-1001"
	Label     string            `json:"label,omitempty"`
	DefinedAt string            `json:"definedAt,omitempty"`
	Location  string            `json:"location,omitempty"`
	Cause     string            `json:"cause,omitempty"`
	Excerpt   []string          `json:"excerpt,omitempty"`
	Hint      string            `json:"hint,omitempty"`
	Log       string            `json:"log,omitempty"`
	Args      map[string]string `json:"args,omitempty"`
}

// LoadCapture reads dir/meta.json.
func LoadCapture(dir string) (*Capture, error) {
	f, err := os.Open(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("translate: capture: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMetaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("translate: capture: %w", err)
	}
	if len(b) > maxMetaBytes {
		return nil, fmt.Errorf("translate: capture: meta.json is larger than %d bytes", maxMetaBytes)
	}
	var c Capture
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("translate: capture: meta.json: %w", err)
	}
	c.dir = dir
	return &c, nil
}

// openStream opens the recorded stderr; a capture without one (Nix never ran) is empty.
func (c *Capture) openStream() (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(c.dir, "stream.log"))
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("translate: capture: %w", err)
	}
	gz, err := os.Open(filepath.Join(c.dir, "stream.log.gz"))
	if errors.Is(err, fs.ErrNotExist) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if err != nil {
		return nil, fmt.Errorf("translate: capture: %w", err)
	}
	zr, err := gzip.NewReader(gz)
	if err != nil {
		gz.Close()
		return nil, fmt.Errorf("translate: capture: stream.log.gz: %w", err)
	}
	return struct {
		io.Reader
		io.Closer
	}{zr, gz}, nil
}

// Replay translates the capture in dir and returns the report and the run's events
// (run.start … run.finish). Log events are left out (they appear only with --json-log).
func Replay(dir string) (*Report, []Event, error) {
	c, err := LoadCapture(dir)
	if err != nil {
		return nil, nil, err
	}
	if c.Precheck != nil || c.Context == nil {
		r := &Report{hints: map[*diag.Error]string{}}
		var evs []Event
		for _, ce := range c.Precheck {
			e, hint := ce.diag()
			if hint != e.Hint() {
				r.hints[e] = hint
			}
			r.Errors = append(r.Errors, e)
			evs = append(evs, Event{Event: "error", Error: e, Hint: hint})
		}
		r.ExitCode = diag.ExitCode(r.Errors)
		return r, append(evs, FinalEvents(r)...), nil
	}
	evs := []Event{{Event: "run.start", Requested: c.Context.Requested}}
	t := New(c.Context)
	t.Sink = func(e Event) {
		if e.Event != "log" {
			evs = append(evs, e)
		}
	}
	if err := Run(t, c); err != nil {
		return nil, nil, err
	}
	r := t.Finish(c.Outcome)
	return r, append(evs, FinalEvents(r)...), nil
}

// Run feeds the capture's stream to t.
func Run(t *Translator, c *Capture) error {
	rc, err := c.openStream()
	if err != nil {
		return err
	}
	defer rc.Close()
	return FeedAll(t, rc)
}

// FeedAll feeds every line of r (Nix's stderr) to t. A line longer than MaxLineBytes is cut
// and reported as drift.
func FeedAll(t *Translator, r io.Reader) error {
	return scanLines(r, func(line []byte, err error) {
		if err != nil {
			t.driftOnce("stderr line longer than " + strconv.Itoa(MaxLineBytes) + " bytes")
			return
		}
		t.Feed(line)
	})
}

// diag converts a recorded error. The hint is the recorded one, or the code's default.
func (ce CapturedError) diag() (*diag.Error, string) {
	n, err := strconv.Atoi(strings.TrimPrefix(ce.Code, "BOB-E-"))
	code := diag.Code(n)
	if _, ok := diag.Lookup(code); err != nil || !ok || !strings.HasPrefix(ce.Code, "BOB-E-") {
		code = cUnknown
	}
	args := map[string]string{}
	maps.Copy(args, ce.Args)
	e := newErr(code, args)
	if ce.Label != "" {
		e.Label = ce.Label
	}
	e.DefinedAt = ParsePos(ce.DefinedAt)
	if ce.Location != "" {
		p := ParsePos(ce.Location)
		e.Location = &p
	}
	e.Cause, e.Excerpt, e.Log = ce.Cause, ce.Excerpt, ce.Log
	hint := ce.Hint
	if hint == "" {
		hint = e.Hint()
	}
	return e, hint
}
