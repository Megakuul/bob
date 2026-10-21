// Package errorsuite is the error-quality suite (HARNESS.md §5): it runs bob against every entry
// of the fault catalogue in tests/faults/ and checks the error code, the exit status, the Go
// source position, that no raw Nix text leaks, the -v superset, the golden and the --json stream.
//
// The directory is tests/errors (suite "errors"); the package is named errorsuite so that it does
// not shadow the standard library's errors package.
package errorsuite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FaultsDir is the catalogue directory relative to the repository root.
const FaultsDir = "tests/faults"

// Inject describes how a fault is put into a fixture (HARNESS.md §5.1).
type Inject struct {
	// Kind is patch, env, cache-fault, limit, signal, disk, ir-script or none.
	Kind     string `json:"kind"`
	File     string `json:"file,omitempty"`
	Describe string `json:"describe"`
}

// Position is the expected position of a recorded fault (replay mode), taken from the capture.
type Position struct {
	DefinedAt string `json:"definedAt,omitempty"` // "package defined at <bob.go>:<line>:<col>"
	Site      string `json:"site,omitempty"`      // "--> <file>:<line>:<col>"
}

// Fault is one tests/faults/<id>/fault.json.
type Fault struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Source  string   `json:"source"`
	Fixture string   `json:"fixture"` // tests/fixtures/repos/<fixture>
	Inject  Inject   `json:"inject"`
	Binary  string   `json:"binary,omitempty"` // "bob" (default) or "bob-server"
	Command []string `json:"command"`
	Code    string   `json:"code"`
	Exit    *int     `json:"exit,omitempty"`
	// Position is "marker" (live: the //bob:fault <id> marker in the fixture's bob.go, and a
	// bob:fault-site marker in the patch) or "none".
	Position       string    `json:"position"`
	ExpectPosition *Position `json:"expectPosition,omitempty"`
	Excerpt        bool      `json:"excerpt"`
	Hint           bool      `json:"hint"`
	NixVer         []string  `json:"nixver"`
	// Forbid lists raw Nix texts of this fault (from its stream) that must not reach the default
	// output, on top of the shared leak patterns.
	Forbid []string `json:"forbid"`
	// Absent lists paths (relative to the fixture copy) that must not exist after the run, e.g.
	// the marker a denied lifecycle script would have written.
	Absent []string `json:"absent,omitempty"`

	Dir string `json:"-"` // absolute entry directory
}

// Outcome is one allowed result: the codes that must appear and the exit status.
type Outcome struct {
	Codes []string `json:"codes"`
	Exit  *int     `json:"exit,omitempty"` // nil: any non-zero status
}

// Expect is one row of the protected index tests/faults/expect.json.
type Expect struct {
	Outcome
	Warnings []string  `json:"warnings,omitempty"` // must appear as warning[BOB-E-NNNN]
	Messages []string  `json:"messages,omitempty"` // substrings of the default output
	Alt      []Outcome `json:"alt,omitempty"`      // other allowed outcomes (fault 34 only)
}

// Outcomes returns the allowed outcomes, the primary one first.
func (e Expect) Outcomes() []Outcome { return append([]Outcome{e.Outcome}, e.Alt...) }

// Entry is a catalogue entry with its expectation.
type Entry struct {
	Fault
	Expect Expect
}

// Load reads expect.json and every fault.json below root/tests/faults. Every directory must have
// a fault.json whose id equals the directory name and an expect.json row, and every row must
// have a directory.
func Load(root string) ([]Entry, error) {
	dir := filepath.Join(root, FaultsDir)
	b, err := os.ReadFile(filepath.Join(dir, "expect.json"))
	if err != nil {
		return nil, fmt.Errorf("fault catalogue: %w", err)
	}
	var expect map[string]Expect
	if err := json.Unmarshal(b, &expect); err != nil {
		return nil, fmt.Errorf("fault catalogue: expect.json: %w", err)
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("fault catalogue: %w", err)
	}
	var out []Entry
	seen := map[string]bool{}
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		p := filepath.Join(dir, de.Name(), "fault.json")
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("fault catalogue: %w", err)
		}
		var f Fault
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("fault catalogue: %s: %w", p, err)
		}
		if f.ID != de.Name() {
			return nil, fmt.Errorf("fault catalogue: %s: id %q differs from its directory", p, f.ID)
		}
		e, ok := expect[f.ID]
		if !ok {
			return nil, fmt.Errorf("fault catalogue: %s has no row in expect.json", f.ID)
		}
		f.Dir = filepath.Join(dir, de.Name())
		if f.Binary == "" {
			f.Binary = "bob"
		}
		seen[f.ID] = true
		out = append(out, Entry{Fault: f, Expect: e})
	}
	var missing []string
	for id := range expect {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("fault catalogue: expect.json rows without a directory: %v", missing)
	}
	return out, nil
}

// ReplayDir returns the recorded capture of nixver, or "" when the entry has none. A capture is a
// meta.json (bob's context) and, when Nix ran, a stream.log or stream.log.gz.
func (f Fault) ReplayDir(nixver string) string {
	d := filepath.Join(f.Dir, nixver)
	if _, err := os.Stat(filepath.Join(d, "meta.json")); err != nil {
		return ""
	}
	return d
}
