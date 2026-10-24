package translate

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bobspike/s15/nixlog"
)

// Fixture is a captured run: meta.json next to stream.log (raw Nix stderr).
type Fixture struct {
	Name     string   `json:"name"`
	Nix      string   `json:"nix"`
	Command  []string `json:"command,omitempty"`
	Outcome  Outcome  `json:"outcome"`
	Context  *Context `json:"context,omitempty"`
	Precheck []*Error `json:"precheck,omitempty"` // Go pre-check errors (Nix never ran)
	Stdout   []string `json:"stdout,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// LoadFixture reads dir/meta.json and dir/stream.log.
func LoadFixture(dir string) (*Fixture, []byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, nil, err
	}
	var f Fixture
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, nil, err
	}
	stream, err := os.ReadFile(filepath.Join(dir, "stream.log"))
	if os.IsNotExist(err) { // large captures are stored gzipped
		if gz, gerr := os.Open(filepath.Join(dir, "stream.log.gz")); gerr == nil {
			defer gz.Close()
			zr, zerr := gzip.NewReader(gz)
			if zerr != nil {
				return nil, nil, zerr
			}
			stream, err = io.ReadAll(zr)
		} else {
			err = nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return &f, stream, nil
}

// Replay translates a fixture: the report plus the --json event stream.
func Replay(f *Fixture, stream []byte) (*Report, []Event) {
	var evs []Event
	if f.Precheck != nil || f.Context == nil {
		r := &Report{Errors: f.Precheck, ExitCode: ExitCode(f.Precheck)}
		for _, e := range f.Precheck {
			evs = append(evs, Event{Event: "error", Error: e})
		}
		return r, append(evs, FinalEvents(r)...)
	}
	evs = append(evs, Event{Event: "run.start", Requested: f.Context.Requested})
	t := New(f.Context)
	t.Sink = func(e Event) {
		if e.Event != "log" { // log lines only with --json-log
			evs = append(evs, e)
		}
	}
	_ = nixlog.Scan(bytes.NewReader(stream), t.Feed)
	r := t.Finish(f.Outcome)
	return r, append(evs, FinalEvents(r)...)
}

// HumanString renders the human output for a style.
func HumanString(r *Report, s Style) string {
	var b strings.Builder
	Human(&b, r, s)
	return b.String()
}
