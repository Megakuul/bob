package builder

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// TestMkdirAllConcurrent: kinds create directories from several goroutines (go-compile writes
// the archives of a module's packages in parallel under shared parents); a parent another
// goroutine created between the check and the mkdir is not an error (A155).
func TestMkdirAllConcurrent(t *testing.T) {
	for round := range 20 {
		out := filepath.Join(t.TempDir(), "out")
		c := &Ctx{Job: bspec.Job{Outputs: []string{"out"}}, Outputs: map[string]string{"out": out}, BuildTop: t.TempDir()}
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.MkdirAll(filepath.Join(out, "pkg", "example.com", "m", fmt.Sprint(i%4))); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}
