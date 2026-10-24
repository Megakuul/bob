package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/internal/store"
)

// Object keys of the registry in the artifacts bucket (SPEC §05.2.4, §6.7).
const (
	BlobPrefix   = "oci/blobs/sha256/"
	repoPrefix   = "oci/repos/"
	maxIndexBody = 4 << 20
	maxManifest  = 4 << 20
	indexCAS     = 10
	// maxBuildTags bounds the per-build tags (b-…) one repository keeps; the oldest go first.
	maxBuildTags = 500
)

var (
	// repoRE is the distribution-spec repository name; bob's are "<project>/<image>".
	repoRE   = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)+$`)
	tagRE    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ValidRepo reports whether repo is a repository name bob serves: "<project>/<image>",
// distribution-spec syntax, ≤ 255 bytes.
func ValidRepo(repo string) bool { return len(repo) <= 255 && repoRE.MatchString(repo) }

// ValidTag reports whether tag is a valid OCI tag.
func ValidTag(tag string) bool { return tagRE.MatchString(tag) }

// ValidDigest reports whether d is "sha256:<64 hex>".
func ValidDigest(d string) bool { return digestRE.MatchString(d) }

// RepoProject is the project of a repository name.
func RepoProject(repo string) string { p, _, _ := strings.Cut(repo, "/"); return p }

// BlobKey is the served object of digest ("sha256:<hex>").
func BlobKey(digest string) string { return BlobPrefix + strings.TrimPrefix(digest, "sha256:") }

// TagEntry is one tag of oci/repos/<repo>/index.json.
type TagEntry struct {
	D string `json:"d"`           // manifest or index digest
	R string `json:"r,omitempty"` // release id ("" for a build tag)
	T int64  `json:"t"`           // unix seconds
	M string `json:"m,omitempty"` // media type of the manifest
	B string `json:"b,omitempty"` // build that wrote a build tag
}

// RepoIndex is oci/repos/<repo>/index.json: {"v":1,"tags":{…}} (CAS-written).
type RepoIndex struct {
	V    int                 `json:"v"`
	Tags map[string]TagEntry `json:"tags"`
}

// Repos reads and CAS-writes the repository indexes.
type Repos struct{ Objects *store.Objects }

func repoKey(repo string) string { return repoPrefix + repo + "/index.json" }

// Get returns the index of repo (nil when absent) and its ETag.
func (r Repos) Get(ctx context.Context, repo string) (*RepoIndex, string, error) {
	if !ValidRepo(repo) {
		return nil, "", nil
	}
	b, etag, err := r.Objects.Read(ctx, repoKey(repo), maxIndexBody)
	if errors.Is(err, store.ErrObjectNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var x RepoIndex
	if err := json.Unmarshal(b, &x); err != nil || x.V != 1 {
		return nil, "", fmt.Errorf("repository index %s is damaged", repoKey(repo))
	}
	if x.Tags == nil {
		x.Tags = map[string]TagEntry{}
	}
	return &x, etag, nil
}

// Update applies fn to the index of repo (a fresh one when absent) and writes it with
// If-None-Match / If-Match, re-reading on a conflict. fn returns false when nothing changes.
func (r Repos) Update(ctx context.Context, repo string, fn func(*RepoIndex) (bool, error)) error {
	if !ValidRepo(repo) {
		return fmt.Errorf("invalid repository name %.300q", repo)
	}
	for range indexCAS {
		cur, etag, err := r.Get(ctx, repo)
		if err != nil {
			return err
		}
		next := &RepoIndex{V: 1, Tags: map[string]TagEntry{}}
		if cur != nil {
			next.Tags = maps.Clone(cur.Tags)
		}
		changed, err := fn(next)
		if err != nil || !changed {
			return err
		}
		body, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if len(body) > maxIndexBody {
			return fmt.Errorf("repository index of %s would exceed %d bytes", repo, maxIndexBody)
		}
		_, err = r.Objects.Put(ctx, repoKey(repo), body, "application/json", store.Cond{IfNoneMatch: cur == nil, IfMatch: etag})
		if errors.Is(err, store.ErrPrecondition) {
			continue
		}
		return err
	}
	return fmt.Errorf("repository index of %s: %w after %d attempts", repo, store.ErrPrecondition, indexCAS)
}

// ErrTagExists: a release tag is create-only within the repository index.
var ErrTagExists = errors.New("tag exists")

// SetBuildTag writes the per-build tag of a promoted image (§6.6.4 step 4): tag = build id.
// An existing tag of the same build keeps its first digest. The oldest build tags beyond
// maxBuildTags are dropped (release tags are never dropped).
func (r Repos) SetBuildTag(ctx context.Context, repo, build, digest, mediaType string, at int64) error {
	if !ValidTag(build) {
		return fmt.Errorf("build id %.100q is not a valid tag", build)
	}
	return r.Update(ctx, repo, func(x *RepoIndex) (bool, error) {
		if _, ok := x.Tags[build]; ok {
			return false, nil
		}
		x.Tags[build] = TagEntry{D: digest, T: at, M: mediaType, B: build}
		var builds []string
		for t, e := range x.Tags {
			if e.R == "" && t != "latest" {
				builds = append(builds, t)
			}
		}
		if len(builds) > maxBuildTags {
			sort.Slice(builds, func(i, j int) bool {
				a, b := x.Tags[builds[i]], x.Tags[builds[j]]
				return a.T < b.T || a.T == b.T && builds[i] < builds[j]
			})
			for _, t := range builds[:len(builds)-maxBuildTags] {
				delete(x.Tags, t)
			}
		}
		return true, nil
	})
}

// SetReleaseTag writes the release tag (create-only: the same tag of another release or
// digest is ErrTagExists) and moves "latest" to the highest non-pre-release version
// (§6.7).
func (r Repos) SetReleaseTag(ctx context.Context, repo, tag, digest, mediaType, release string, at int64) error {
	if !ValidTag(tag) || tag == "latest" {
		return fmt.Errorf("invalid release tag %.130q", tag)
	}
	return r.Update(ctx, repo, func(x *RepoIndex) (bool, error) {
		changed := false
		if e, ok := x.Tags[tag]; ok {
			if e.R != release || e.D != digest {
				return false, fmt.Errorf("%w: %s:%s", ErrTagExists, repo, tag)
			}
		} else {
			x.Tags[tag] = TagEntry{D: digest, R: release, T: at, M: mediaType}
			changed = true
		}
		return moveLatest(x) || changed, nil
	})
}

// RemoveRelease drops every tag of release (DeleteRelease) and moves "latest".
func (r Repos) RemoveRelease(ctx context.Context, repo, release string) error {
	return r.Update(ctx, repo, func(x *RepoIndex) (bool, error) {
		changed := false
		for t, e := range x.Tags {
			if e.R == release && t != "latest" {
				delete(x.Tags, t)
				changed = true
			}
		}
		return moveLatest(x) || changed, nil
	})
}

// moveLatest points "latest" at the highest release tag that is a version without
// pre-release part; without one "latest" is removed.
func moveLatest(x *RepoIndex) bool {
	var best Version
	bestTag := ""
	for t, e := range x.Tags {
		v, ok := ParseVersion(t)
		if e.R == "" || t == "latest" || !ok || v.Pre != "" {
			continue
		}
		if bestTag == "" || v.Compare(best) > 0 {
			best, bestTag = v, t
		}
	}
	cur, had := x.Tags["latest"]
	if bestTag == "" {
		if had {
			delete(x.Tags, "latest")
			return true
		}
		return false
	}
	want := x.Tags[bestTag]
	if had && cur == want {
		return false
	}
	x.Tags["latest"] = want
	return true
}

// SortedTags returns the tag names of x, sorted.
func (x *RepoIndex) SortedTags() []string {
	if x == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(x.Tags))
}
