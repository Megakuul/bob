package logstore

import (
	"strings"
	"testing"
)

// TestSizeTriggerCountsLineText: the 2 MiB commit trigger of R-LOG-001 counts the buffered
// lines (their text), not the row overhead the memory bound adds: 2,000 lines of 1,000 bytes
// stay below it, 2,100 reach it (A293).
func TestSizeTriggerCountsLineText(t *testing.T) {
	const build = "b-01M5N8WSK5HJ31JASXZEXY8FY4"
	w := NewWriter(WriterConfig{})
	text := strings.Repeat("x", 1000)
	add := func(from, n int) {
		lines := make([]Line, 0, n)
		for i := range n {
			lines = append(lines, Line{Package: "//big:t", Seq: int64(from + i), Attempt: 1, Text: text})
		}
		if err := w.Add(build, "", lines...); err != nil {
			t.Fatal(err)
		}
	}
	urgent := func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, p := range w.parts {
			if p.urgent {
				return true
			}
		}
		return false
	}
	add(1, 2000)
	if urgent() {
		t.Fatal("2,000 lines of 1,000 bytes (< 2 MiB of lines) already triggered a commit")
	}
	add(2001, 100)
	if !urgent() {
		t.Fatal("2,100 lines of 1,000 bytes (> 2 MiB of lines) did not trigger a commit")
	}
}
