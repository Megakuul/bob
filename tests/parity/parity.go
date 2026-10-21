// Package parity maps tests to SPEC requirement ids (HARNESS.md §11).
//
// A test that checks a requirement calls Covers. The parity generator in
// tools/internal/parity finds these calls statically, so a requirement whose
// test did not run is reported as "not run" instead of disappearing. At run
// time Covers appends the test's result to $BOB_PARITY_OUT.
package parity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// EnvOut names the file Covers appends results to. tools/ci sets it.
const EnvOut = "BOB_PARITY_OUT"

// Result is one line of the results file.
type Result struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"`    // absolute directory of the calling file
	Test   string `json:"test"`   // top-level test name
	Result string `json:"result"` // pass, fail or skip
}

// Covers records that t checks the requirements ids. The id strings must be
// literals: the generator reads them from the source.
func Covers(t testing.TB, ids ...string) {
	t.Helper()
	out := os.Getenv(EnvOut)
	if out == "" {
		return
	}
	dir := ""
	if _, file, _, ok := runtime.Caller(1); ok {
		dir = filepath.Dir(file)
	}
	// Subtests report under their top-level test; the generator only knows
	// the function name.
	test, _, _ := strings.Cut(t.Name(), "/")
	t.Cleanup(func() {
		res := "pass"
		switch {
		case t.Failed():
			res = "fail"
		case t.Skipped():
			res = "skip"
		}
		var b []byte
		for _, id := range ids {
			line, err := json.Marshal(Result{ID: id, Dir: dir, Test: test, Result: res})
			if err != nil {
				t.Errorf("parity: %v", err)
				return
			}
			b = append(append(b, line...), '\n')
		}
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Errorf("parity: %v", err)
			return
		}
		// One write call per test keeps lines of parallel test binaries intact.
		if _, err := f.Write(b); err != nil {
			t.Errorf("parity: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Errorf("parity: %v", err)
		}
	})
}
