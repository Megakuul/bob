package cache

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Visibility documents (SPEC §6.6.2, D58): one per served object, written only by
// orchestrators: create If-None-Match, update If-Match.
const (
	NixVisPrefix = "nix/vis/"
	OCIVisPrefix = "oci/vis/sha256/"

	VisTTL         = 30 * time.Second // revoking public visibility takes effect within it
	visNegativeTTL = 2 * time.Second  // a just-promoted path shows up quickly elsewhere
	visMaxEntries  = 200_000
	visMaxCAS      = 5
	visMaxBody     = 2 << 20
	MaxPublic      = 10_000
)

// ErrTooManyReleases: the public list of a document is full (SPEC §6.6.2).
var ErrTooManyReleases = errors.New("too many releases share this object")

// Visibility is a visibility document: {"v":1,"projects":[…],"public":[<release id>…]}.
type Visibility struct {
	V        int      `json:"v"`
	Projects []string `json:"projects"`
	Public   []string `json:"public"`
}

// VisStore reads (cached) and writes the visibility documents below one prefix.
type VisStore struct {
	obj    *Objects
	prefix string
	now    func() time.Time

	mu       sync.Mutex
	lru      *list.List // of *visEntry, most recent first
	entries  map[string]*list.Element
	inflight map[string]*visCall
}

type visEntry struct {
	id      string
	doc     *Visibility // nil = absent
	expires time.Time
}

type visCall struct {
	done chan struct{}
	doc  *Visibility
	err  error
}

// NewVisStore returns the documents below prefix (NixVisPrefix or OCIVisPrefix).
func NewVisStore(obj *Objects, prefix string, now func() time.Time) *VisStore {
	if now == nil {
		now = time.Now
	}
	return &VisStore{obj: obj, prefix: prefix, now: now, lru: list.New(),
		entries: map[string]*list.Element{}, inflight: map[string]*visCall{}}
}

func (s *VisStore) key(id string) string { return s.prefix + id + ".json" }

// Get returns the document of id (nil when absent), from the cache when it is younger than
// VisTTL. Concurrent misses of one id share one GET.
func (s *VisStore) Get(ctx context.Context, id string) (*Visibility, error) {
	s.mu.Lock()
	if el, ok := s.entries[id]; ok {
		e := el.Value.(*visEntry)
		if s.now().Before(e.expires) {
			s.lru.MoveToFront(el)
			s.mu.Unlock()
			return e.doc, nil
		}
	}
	if c, ok := s.inflight[id]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.doc, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &visCall{done: make(chan struct{})}
	s.inflight[id] = c
	s.mu.Unlock()

	c.doc, _, c.err = s.read(ctx, id)
	s.mu.Lock()
	delete(s.inflight, id)
	if c.err == nil {
		s.store(id, c.doc)
	}
	s.mu.Unlock()
	close(c.done)
	return c.doc, c.err
}

// store caches doc; s.mu is held.
func (s *VisStore) store(id string, doc *Visibility) {
	ttl := VisTTL
	if doc == nil {
		ttl = visNegativeTTL
	}
	e := &visEntry{id: id, doc: doc, expires: s.now().Add(ttl)}
	if el, ok := s.entries[id]; ok {
		el.Value = e
		s.lru.MoveToFront(el)
		return
	}
	s.entries[id] = s.lru.PushFront(e)
	for s.lru.Len() > visMaxEntries {
		last := s.lru.Back()
		s.lru.Remove(last)
		delete(s.entries, last.Value.(*visEntry).id)
	}
}

// Invalidate drops the cached document of id (after a local write).
func (s *VisStore) Invalidate(id string) {
	s.mu.Lock()
	if el, ok := s.entries[id]; ok {
		s.lru.Remove(el)
		delete(s.entries, id)
	}
	s.mu.Unlock()
}

// read GETs the document; absent is (nil, "", nil).
func (s *VisStore) read(ctx context.Context, id string) (*Visibility, string, error) {
	b, etag, err := s.obj.Read(ctx, s.key(id), visMaxBody)
	if errors.Is(err, ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var v Visibility
	if err := json.Unmarshal(b, &v); err != nil || v.V != 1 {
		return nil, "", fmt.Errorf("visibility document %s is damaged", s.key(id))
	}
	return &v, etag, nil
}

// Update applies fn to the document of id (a fresh empty one when absent) and writes the
// result with If-None-Match / If-Match, re-reading on a conflict (≤ 5 attempts). fn returns
// false when nothing changes.
func (s *VisStore) Update(ctx context.Context, id string, fn func(*Visibility) (bool, error)) error {
	for attempt := 0; attempt < visMaxCAS; attempt++ {
		cur, etag, err := s.read(ctx, id)
		if err != nil {
			return err
		}
		next := Visibility{V: 1}
		if cur != nil {
			next = *cur
			next.Projects = slices.Clone(cur.Projects)
			next.Public = slices.Clone(cur.Public)
		}
		changed, err := fn(&next)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if next.Projects == nil {
			next.Projects = []string{}
		}
		if next.Public == nil {
			next.Public = []string{}
		}
		body, err := json.Marshal(next)
		if err != nil {
			return err
		}
		c := Cond{IfNoneMatch: cur == nil, IfMatch: etag}
		_, err = s.obj.Put(ctx, s.key(id), body, "application/json", c)
		if errors.Is(err, ErrPrecondition) {
			continue
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.store(id, &next)
		s.mu.Unlock()
		return nil
	}
	return fmt.Errorf("visibility document %s: %w after %d attempts", s.key(id), ErrPrecondition, visMaxCAS)
}

// AddProject adds project to the document of id (promotion).
func (s *VisStore) AddProject(ctx context.Context, id, project string) error {
	return s.Update(ctx, id, func(v *Visibility) (bool, error) {
		if slices.Contains(v.Projects, project) {
			return false, nil
		}
		v.Projects = append(v.Projects, project)
		slices.Sort(v.Projects)
		return true, nil
	})
}

// SetPublic adds (public) or removes the release id from the document of id (releases,
// §6.8). Removing from an absent document is a no-op.
func (s *VisStore) SetPublic(ctx context.Context, id, release string, public bool) error {
	return s.Update(ctx, id, func(v *Visibility) (bool, error) {
		i := slices.Index(v.Public, release)
		switch {
		case public && i >= 0, !public && i < 0:
			return false, nil
		case public && len(v.Public) >= MaxPublic:
			return false, ErrTooManyReleases
		case public:
			v.Public = append(v.Public, release)
			slices.Sort(v.Public)
		default:
			v.Public = slices.Delete(v.Public, i, i+1)
		}
		return true, nil
	})
}
