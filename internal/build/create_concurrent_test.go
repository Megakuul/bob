package buildctl_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/megakuul/bob/tests/store/env"
)

// TestConcurrentCreate: 30 builds of one project created at the same moment all succeed with
// distinct numbers (A297: the shared pctr, bidx/running and build page CAS lost all rounds).
func TestConcurrentCreate(t *testing.T) {
	l := newLab(t, env.Start(t))
	const n = 30
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], errs[i] = l.b.CreateFromEvent(l.ctx, "acme", fmt.Sprintf("k%d", i), push("main"))
		}()
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Errorf("create %d: %v", i, errs[i])
			continue
		}
		num := l.build(ids[i]).Number.Value()
		if seen[num] {
			t.Errorf("build number %d given twice", num)
		}
		seen[num] = true
	}
}
