package project_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/tests/store/env"
)

// TestForUploadConcurrent: 40 first remote builds of one project at the same moment all get
// the project (A297: the implicit creation lost its slot/index CAS rounds).
func TestForUploadConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	p, err := project.New(project.Config{Control: be.Open(t, "control"), BaseURL: "https://bob.example"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const n = 40
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = p.ForUpload(ctx, "fanout")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("ForUpload %d: %v", i, err)
		}
	}
}
