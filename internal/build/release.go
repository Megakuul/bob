package buildctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/store"
)

// ReleaseRequest is one release a succeeded build publishes (SPEC §6.8): a release of a
// matched release pipeline (its definition names releases by name, "api", or label,
// "//rel:api") and the candidate build nodes: those whose label is it or ends in ":<name>"
// (the release's node is the one whose output holds release.json).
type ReleaseRequest struct {
	Build    *store.BuildRecord
	Pipeline string
	Label    string // the pipeline's release entry (name or label)
	Nodes    []Node
	// Tag is the git tag the build ran for ("" when the build has no tag event): the release
	// version is the tag without a leading "v".
	Tag string
}

// Releaser publishes the release of a request and returns its id. It is idempotent: called
// again for the same build and label it continues (or confirms) the same release. An error
// that is a *ReleaseFailure fails the build with its message; any other error is retried on
// the next controller pass.
type Releaser interface {
	Publish(ctx context.Context, r ReleaseRequest) (string, error)
}

// ReleaseFailure is a release that cannot be published (already exists, manifest rejected,
// asset expired): the build fails with Msg.
type ReleaseFailure struct{ Msg string }

func (f *ReleaseFailure) Error() string { return f.Msg }

// SetReleaser installs the release controller hook. Call it before the controller runs.
func (b *Builds) SetReleaser(r Releaser) { b.releaser = r }

// release publishes the releases of the matched release pipelines of a build whose jobs all
// succeeded, before the build is marked succeeded (§05.6.4 "build succeeded" guard; the
// promotion of every job is done by then). It records each release id in its pipeline run.
func (b *Builds) release(ctx context.Context, rec *store.BuildRecord) (*store.BuildRecord, error) {
	if b.releaser == nil {
		return rec, nil
	}
	var nodes []Node
	loaded := false
	tag := ""
	if sel := SelectionOf(rec); sel.Event == jobspec.EventTag {
		tag = sel.Ref
	}
	for _, p := range rec.Pipelines.Value() {
		if !p.Releases || p.Release != "" || len(p.ReleaseLabels) == 0 {
			continue
		}
		if !loaded {
			bn, err := store.Get(ctx, b.cfg.Control, &BuildNodes{ID: rec.ID})
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return rec, err
			}
			if bn != nil {
				nodes = bn.Nodes.Value()
			}
			loaded = true
		}
		first := ""
		for _, label := range p.ReleaseLabels {
			var mine []Node
			for _, n := range nodes {
				if len(n.Outputs) > 0 && (n.Status == "built" || n.Status == "cached") && releaseNode(n.Label, label) {
					mine = append(mine, n)
				}
			}
			if len(mine) == 0 {
				return rec, &ReleaseFailure{Msg: fmt.Sprintf("release %s of pipeline %s was not built", label, p.Name)}
			}
			id, err := b.releaser.Publish(ctx, ReleaseRequest{Build: rec, Pipeline: p.Name, Label: label, Nodes: mine, Tag: tag})
			if err != nil {
				return rec, err
			}
			if first == "" {
				first = id
			}
		}
		name := p.Name
		next, err := b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
			pipes := slices.Clone(cur.Pipelines.Value())
			i := slices.IndexFunc(pipes, func(x store.PipelineRun) bool { return x.Name == name })
			if i < 0 || pipes[i].Release == first {
				return store.ErrNoChange
			}
			pipes[i].Release = first
			cur.Pipelines = store.Set(pipes)
			return nil
		})
		if err != nil {
			return rec, err
		}
		rec = next
	}
	return rec, nil
}

// releaseNode reports whether a node label can be the release entry's node: the label itself,
// or a label whose name (after ":") is the release name.
func releaseNode(nodeLabel, entry string) bool {
	if nodeLabel == entry {
		return true
	}
	if strings.HasPrefix(entry, "//") {
		return false
	}
	_, name, ok := strings.Cut(nodeLabel, ":")
	return ok && name == entry
}
