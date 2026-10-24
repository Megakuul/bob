package rbac

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
)

// Resolver maps the ids of KIND_PROJECT rules with a resolve option to their project
// (api.Resolver). Each lookup is one GET of a known key (R-API-151); a missing resource, or an
// id that cannot name one, is NotFound "<what> not found".
type Resolver struct {
	Control *store.Bucket
	Fleet   *store.Bucket // job records while the job is not finalised; nil: jobs are not resolvable
	// Job, when set, resolves job ids instead of the fleet job record (e.g. once finalised
	// jobs are kept elsewhere). It returns store.ErrNotFound for an unknown job.
	Job func(ctx context.Context, id string) (string, error)
}

var _ api.Resolver = (*Resolver)(nil)

// Project implements api.Resolver. kind is "build", "job", "release" or "repository".
func (r *Resolver) Project(ctx context.Context, kind, id string) (string, error) {
	p, err := r.project(ctx, kind, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrKeySegment):
		return "", api.NotFound(kind)
	case err != nil:
		return "", fmt.Errorf("rbac: resolve %s %q: %w", kind, id, err)
	case p == "":
		return "", api.NotFound(kind)
	}
	return p, nil
}

// releaseHead reads only the existence of rel/<projectID>/n/<num10>.
type releaseHead struct {
	Project store.KeyField `pk:"rel" cbor:"-"`
	N       store.KeyField `sk:"n" cbor:"-"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *releaseHead) Draft() *releaseHead {
	return &releaseHead{
		Project: r.Project,
		N:       r.N,
	}
}

func (r *Resolver) project(ctx context.Context, kind, id string) (string, error) {
	if kind != "repository" {
		if _, err := store.Segment(id); err != nil {
			return "", err
		}
	}
	switch kind {
	case "build":
		b, err := store.Get(ctx, r.Control, &store.BuildRecord{ID: store.ID(id)})
		if err != nil {
			return "", err
		}
		return b.Project.Value(), nil
	case "job":
		if r.Job != nil {
			return r.Job(ctx, id)
		}
		if r.Fleet == nil {
			return "", store.ErrNotFound
		}
		j, err := store.Get(ctx, r.Fleet, &store.JobRecord{ID: store.ID(id)})
		if err != nil {
			return "", err
		}
		var spec store.JobSpec
		if err := cbor.Unmarshal(j.Spec.Value(), &spec); err != nil {
			return "", fmt.Errorf("decode job spec: %w", err)
		}
		return spec.Project, nil
	case "release":
		// release id = <projectID>.<num10> (store.ReleaseID); the record proves it exists.
		project, num, ok := strings.Cut(id, ".")
		n, err := strconv.ParseUint(num, 10, 64)
		if !ok || err != nil || len(num) != 10 {
			return "", store.ErrNotFound
		}
		if !ValidProject(project) || project == Any {
			return "", store.ErrNotFound
		}
		numStr, _ := store.Num10(n)
		if _, err := store.Get(ctx, r.Control, &releaseHead{Project: store.ID(project), N: store.ID(numStr)}); err != nil {
			return "", err
		}
		return project, nil
	case "repository":
		// "<project>/<image>": the handler answers NotFound for an image that does not exist.
		project, image, ok := strings.Cut(id, "/")
		if !ok || image == "" || !ValidProject(project) || project == Any {
			return "", store.ErrNotFound
		}
		return project, nil
	}
	return "", fmt.Errorf("unknown resource kind %q", kind)
}
