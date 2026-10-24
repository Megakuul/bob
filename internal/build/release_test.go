package buildctl_test

import (
	"context"
	"slices"
	"testing"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/tests/store/env"
)

// releaser records the release requests; fail makes Publish fail the build.
type releaser struct {
	reqs []buildctl.ReleaseRequest
	fail string
}

func (r *releaser) Publish(_ context.Context, req buildctl.ReleaseRequest) (string, error) {
	r.reqs = append(r.reqs, req)
	if r.fail != "" {
		return "", &buildctl.ReleaseFailure{Msg: r.fail}
	}
	return req.Build.Project.Value() + ".0000000001", nil
}

func tag(name string) forge.Event {
	return forge.Event{Type: forge.EventTag, DeliveryID: "d-" + name, Tag: &forge.Tag{Name: name, Commit: sha1}}
}

// TestReleaseHook: a succeeded tag build of a release pipeline publishes its release label
// (with the label's nodes and the tag) before it is marked succeeded and records the release
// id in the pipeline run; a release failure fails the build with its message.
func TestReleaseHook(t *testing.T) {
	be := env.Start(t)
	t.Run("published", func(t *testing.T) {
		l := newLab(t, be)
		r := &releaser{}
		l.b.SetReleaser(r)
		id, err := l.b.CreateFromEvent(l.ctx, "acme", "k1", tag("v1.4.2"))
		if err != nil {
			t.Fatal(err)
		}
		l.pass()
		rep := okReport("release")
		rep.Packages = append(rep.Packages, jobspecPackage("api", "/nix/store/0c4q4b4wjj3nh0p5z2zv0k4hb9hfw2rg-release-api"))
		l.run(l.job(id), "ok", rep)
		l.pass()
		rec := l.build(id)
		if rec.State.Value() != buildctl.StateSucceeded || len(rec.Pipelines.Value()) != 1 || rec.Pipelines.Value()[0].Release != "acme.0000000001" {
			t.Fatalf("build %s pipelines %+v errors %+v", rec.State.Value(), rec.Pipelines.Value(), rec.Errors.Value())
		}
		if len(r.reqs) != 1 || r.reqs[0].Label != "api" || r.reqs[0].Tag != "v1.4.2" || r.reqs[0].Pipeline != "release" ||
			len(r.reqs[0].Nodes) != 1 || !slices.Equal(r.reqs[0].Nodes[0].Outputs, []string{"/nix/store/0c4q4b4wjj3nh0p5z2zv0k4hb9hfw2rg-release-api"}) {
			t.Errorf("release requests %+v", r.reqs)
		}
		if !slices.Equal(rec.Pipelines.Value()[0].ReleaseLabels, []string{"api"}) {
			t.Errorf("release labels %v", rec.Pipelines.Value()[0].ReleaseLabels)
		}
	})
	t.Run("already-exists", func(t *testing.T) {
		l := newLab(t, be)
		msg := "release acme/api@1.4.2 already exists; releases are immutable"
		l.b.SetReleaser(&releaser{fail: msg})
		id, err := l.b.CreateFromEvent(l.ctx, "acme", "k2", tag("v1.4.2"))
		if err != nil {
			t.Fatal(err)
		}
		l.pass()
		rep := okReport("release")
		rep.Packages = append(rep.Packages, jobspecPackage("api", "/nix/store/0c4q4b4wjj3nh0p5z2zv0k4hb9hfw2rg-release-api"))
		l.run(l.job(id), "ok", rep)
		l.pass()
		rec := l.build(id)
		if rec.State.Value() != buildctl.StateFailed || len(rec.Errors.Value()) == 0 || rec.Errors.Value()[0].Message != msg {
			t.Fatalf("build %s errors %+v", rec.State.Value(), rec.Errors.Value())
		}
	})
	t.Run("not-built", func(t *testing.T) {
		l := newLab(t, be)
		l.b.SetReleaser(&releaser{})
		id, err := l.b.CreateFromEvent(l.ctx, "acme", "k3", tag("v2.0.0"))
		if err != nil {
			t.Fatal(err)
		}
		l.pass()
		l.run(l.job(id), "ok", okReport("release")) // no node for the release label
		l.pass()
		if rec := l.build(id); rec.State.Value() != buildctl.StateFailed {
			t.Fatalf("build %s errors %+v", rec.State.Value(), rec.Errors.Value())
		}
	})
}

func jobspecPackage(label, out string) jobspec.PackageReport {
	return jobspec.PackageReport{Label: label, Platform: "aarch64-linux", Status: "built", Outputs: []string{out}}
}
