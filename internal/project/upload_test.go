package project_test

import (
	"context"
	"errors"
	"testing"
	"time"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/tests/store/env"
)

// TestForUpload: the first upload build of an unknown project id creates the project without a
// repository (decision D-IB13-1); repeated and concurrent calls agree; invalid ids stay unknown.
func TestForUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control := be.Open(t, "control")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := project.New(project.Config{Control: control})
	if err != nil {
		t.Fatal(err)
	}
	var _ buildctl.UploadProjects = p

	if _, err := p.Project(ctx, "remote"); !errors.Is(err, buildctl.ErrNoProject) {
		t.Fatalf("Project(remote) before the upload = %v", err)
	}
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			got, err := p.ForUpload(ctx, "remote")
			if err == nil && (got.ID != "remote" || got.Repo != "") {
				err = errors.New("wrong project " + got.ID + " " + got.Repo)
			}
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Errorf("ForUpload(remote): %v", err)
		}
	}
	rec, err := p.Get(ctx, "remote")
	if err != nil || rec.DisplayName.Value() != "remote" || rec.Forge.Value() != "" {
		t.Fatalf("project record after ForUpload: %+v, %v", rec, err)
	}
	ids, _, err := p.List(ctx, 0, 10, func(project.Entry) bool { return true })
	if err != nil || len(ids) != 1 || ids[0].ID != "remote" {
		t.Errorf("List after ForUpload: %+v, %v", ids, err)
	}
	if _, err := p.ForUpload(ctx, "9-not-an-id"); !errors.Is(err, buildctl.ErrNoProject) {
		t.Errorf("ForUpload(invalid id) = %v, want ErrNoProject", err)
	}
}
