package buildctl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/store"
)

// ErrNoBuild is an event that starts no build (deleted branch or tag, closed pull request,
// ping, ignored kinds).
var ErrNoBuild = errors.New("event starts no build")

// FromEvent turns a verified forge event of project's repository into a build request
// (§6.9.4): pushes and tags run the pipelines whose triggers match; pull requests (opened,
// synchronized, reopened) run the pull_request pipelines on the head commit.
func FromEvent(project string, ev forge.Event) (Request, error) {
	r := Request{Project: project, Trigger: store.BuildTrigger{Event: ev.DeliveryID, Sender: ev.Sender}}
	switch {
	case ev.Type == forge.EventPush && ev.Push != nil && !ev.Push.Deleted:
		r.Trigger.Kind, r.Trigger.Ref = TriggerPush, "refs/heads/"+ev.Push.Branch
		r.Branch, r.Commit = ev.Push.Branch, ev.Push.After
		r.Source = store.BuildSource{Ref: r.Trigger.Ref, SHA: ev.Push.After}
	case ev.Type == forge.EventTag && ev.Tag != nil && !ev.Tag.Deleted:
		r.Trigger.Kind, r.Trigger.Ref = TriggerTag, "refs/tags/"+ev.Tag.Name
		r.Commit = ev.Tag.Commit
		r.Source = store.BuildSource{Ref: r.Trigger.Ref, SHA: ev.Tag.Commit}
	case ev.Type == forge.EventPullRequest && ev.PullRequest != nil && ev.PullRequest.Action != forge.PRClosed:
		pr := ev.PullRequest
		ref := fmt.Sprintf("refs/pull/%d/head", pr.Number)
		r.Trigger.Kind, r.Trigger.Ref, r.Trigger.PR, r.Trigger.Fork = TriggerPR, ref, pr.Number, pr.FromFork
		r.Branch, r.Commit = pr.HeadBranch, pr.HeadSHA
		r.Source = store.BuildSource{Ref: ref, SHA: pr.HeadSHA, FetchRef: pr.FetchRef}
	default:
		return r, ErrNoBuild
	}
	return r, nil
}

// buildKey is `bkey/<hash64(key)>`: the build created for one delivery key (idempotent
// CreateFromEvent).
type buildKey struct {
	Hash  store.KeyField             `pk:"bkey" cbor:"-"`
	ETag  store.ETagField            `etag:"true" cbor:"-"`
	Build store.DataField[string]    `cbor:"b,omitempty"`
	At    store.DataField[time.Time] `cbor:"at,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *buildKey) Draft() *buildKey {
	return &buildKey{
		Hash:  r.Hash,
		ETag:  r.ETag,
		Build: store.Carry(r.Build),
		At:    store.Carry(r.At),
	}
}

// CreateFromEvent implements the webhook intake's Builds (internal/forge/integration): it
// creates the build of a verified event, idempotent on key (a delivery a crash left
// unfinished gets the first call's build). Events that start no build return ErrNoBuild.
func (b *Builds) CreateFromEvent(ctx context.Context, project, key string, ev forge.Event) (string, error) {
	r, err := FromEvent(project, ev)
	if err != nil {
		return "", err
	}
	h, err := store.Hash64(key)
	if err != nil {
		return "", err
	}
	id, err := NewBuildID(b.now())
	if err != nil {
		return "", err
	}
	k := &buildKey{Hash: store.ID(h), Build: store.Set(id), At: store.Set(b.now())}
	err = store.Create(ctx, b.cfg.Control, k)
	if errors.Is(err, store.ErrExists) {
		cur, gerr := store.Get(ctx, b.cfg.Control, &buildKey{Hash: store.ID(h)})
		if gerr != nil {
			return "", gerr
		}
		id = cur.Build.Value()
		if _, gerr := b.Get(ctx, id); gerr == nil {
			return id, nil
		} else if !errors.Is(gerr, store.ErrNotFound) {
			return "", gerr
		}
	} else if err != nil {
		return "", err
	}
	r.ID = id
	rec, err := b.Create(ctx, r)
	if err != nil {
		return "", err
	}
	return rec.ID.Value(), nil
}
