//go:build bobtest

package server

import (
	"os"
	"strings"
	"sync"
	"time"
)

// Clock returns the process clock of a -tags bobtest build: the wall clock plus
// BOB_TEST_CLOCK_OFFSET (a Go duration, fixed at start; HARNESS §13.2) plus the duration in
// the file BOB_TEST_CLOCK_FILE (re-read at most every 200 ms; missing or invalid = 0), so
// expiry tests can move time instead of waiting.
func Clock(getenv func(string) string) func() time.Time {
	offset, _ := time.ParseDuration(getenv("BOB_TEST_CLOCK_OFFSET"))
	file := getenv("BOB_TEST_CLOCK_FILE")
	var (
		mu     sync.Mutex
		extra  time.Duration
		readAt time.Time
	)
	return func() time.Time {
		now := time.Now()
		if file == "" {
			return now.Add(offset)
		}
		mu.Lock()
		defer mu.Unlock()
		if now.Sub(readAt) > 200*time.Millisecond {
			readAt = now
			extra = 0
			if b, err := os.ReadFile(file); err == nil {
				if d, err := time.ParseDuration(strings.TrimSpace(string(b))); err == nil {
					extra = d
				}
			}
		}
		return now.Add(offset + extra)
	}
}
