package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/store"
)

// Schema is the release manifest format (SPEC R-OUT-023).
const Schema = "bob.release/v1"

const (
	maxManifestNAR  = 8 << 20 // compressed and decompressed NAR of the manifest's output
	maxManifestJSON = 4 << 20
	maxArtifacts    = 1024
)

var (
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	fileRE    = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
	nameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	hexRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Manifest is release.json (bob.release/v1).
type Manifest struct {
	Artifacts []Artifact `json:"artifacts"`
	Label     string     `json:"label"`
	Project   string     `json:"project"`
	Public    bool       `json:"public"`
	Release   string     `json:"release"`
	Schema    string     `json:"schema"`
	Version   string     `json:"version"`
}

// Artifact is one artifact of the manifest.
type Artifact struct {
	Entrypoints map[string]string `json:"entrypoints,omitempty"`
	File        string            `json:"file,omitempty"`
	Index       string            `json:"index,omitempty"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Platform    string            `json:"platform"`
	Repo        string            `json:"repo,omitempty"`
	SHA256      string            `json:"sha256,omitempty"`
	Size        int64             `json:"size,omitempty"`
	StorePath   string            `json:"storePath"`
	Tags        []string          `json:"tags,omitempty"`
}

// ParseManifest decodes and validates release.json (hostile input: it comes out of a build)
// for project and the pipeline's release entry (a release name, or a label "//pkg:name").
// A manifest with project "" belongs to the building project, and its image repositories are
// relative to it (the returned manifest names them in full).
func ParseManifest(b []byte, project, entry string) (*Manifest, error) {
	if len(b) > maxManifestJSON {
		return nil, errors.New("release.json is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("release.json: %w", err)
	}
	name := entry
	if strings.HasPrefix(entry, "//") {
		_, name, _ = strings.Cut(entry, ":")
	}
	switch {
	case m.Schema != Schema:
		return nil, fmt.Errorf("release.json: schema %.40q, want %s", m.Schema, Schema)
	case strings.HasPrefix(entry, "//") && m.Label != entry:
		return nil, fmt.Errorf("release.json names label %.200q, not %s", m.Label, entry)
	case !strings.HasPrefix(m.Label, "//"):
		return nil, fmt.Errorf("release.json: invalid label %.200q", m.Label)
	case m.Release != name || !nameRE.MatchString(m.Release):
		return nil, fmt.Errorf("release.json names release %.100q, not %q", m.Release, name)
	case m.Project != "" && m.Project != project:
		return nil, fmt.Errorf("release.json belongs to project %.100q, not %s", m.Project, project)
	case !versionRE.MatchString(m.Version):
		return nil, fmt.Errorf("invalid release version %.80q", m.Version)
	case len(m.Artifacts) > maxArtifacts:
		return nil, fmt.Errorf("release.json lists more than %d artifacts", maxArtifacts)
	}
	if _, ok := registry.ParseVersion(m.Version); !ok {
		return nil, fmt.Errorf("release version %q is not a semantic version (major.minor.patch)", m.Version)
	}
	files := map[string]bool{}
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if !nameRE.MatchString(a.Name) || !cache.ValidStorePath(a.StorePath) {
			return nil, fmt.Errorf("release.json: invalid artifact %.100q", a.Name)
		}
		switch a.Kind {
		case "closure":
			if a.Platform != "linux-amd64" && a.Platform != "linux-arm64" {
				return nil, fmt.Errorf("release.json: closure %s has platform %.40q", a.Name, a.Platform)
			}
		case "appimage", "tarball":
			if !fileRE.MatchString(a.File) || a.File == "." || a.File == ".." || !hexRE.MatchString(a.SHA256) || a.Size <= 0 ||
				a.Platform != "linux-amd64" && a.Platform != "linux-arm64" {
				return nil, fmt.Errorf("release.json: invalid %s artifact %s", a.Kind, a.Name)
			}
			if files[a.File] {
				return nil, fmt.Errorf("release.json: file %s listed twice", a.File)
			}
			files[a.File] = true
		case "oci":
			if m.Project == "" {
				// built without a server project (bob.RepoOpts has none): the manifest names the
				// repository relative to the project, the server puts its project in front
				// (decision D-IB20-1; the worker stages the image under the same name)
				a.Repo = project + "/" + a.Repo
			}
			if !registry.ValidRepo(a.Repo) || registry.RepoProject(a.Repo) != project || !registry.ValidDigest(a.Index) {
				return nil, fmt.Errorf("release.json: image %s has repository %.300q or index %.80q (repositories are %s/<image>)", a.Name, a.Repo, a.Index, project)
			}
			if !slices.Contains(a.Tags, m.Version) {
				return nil, fmt.Errorf("release.json: image %s is not tagged %s", a.Name, m.Version)
			}
		default:
			return nil, fmt.Errorf("release.json: unknown artifact kind %.40q", a.Kind)
		}
	}
	return &m, nil
}

// readManifest reads release.json out of the served NAR of storePath (the release
// derivation's output, promoted for project).
func (s *Releases) readManifest(ctx context.Context, project, storePath string) ([]byte, error) {
	if !cache.ValidStorePath(storePath) {
		return nil, failf("release manifest path %.300q is not a store path", storePath)
	}
	hash := cache.HashOf(storePath)
	doc, err := s.c.NixVis.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	if doc == nil || !slices.Contains(doc.Projects, project) {
		return nil, failf("release manifest %s is not in the cache (promotion incomplete)", storePath)
	}
	o, err := s.c.Objects.Get(ctx, cache.NarKey(hash), "", "")
	if errors.Is(err, store.ErrObjectNotFound) {
		return nil, failf("release manifest %s is not in the cache (promotion incomplete)", storePath)
	}
	if err != nil {
		return nil, err
	}
	defer o.Body.Close()
	if o.Length > maxManifestNAR {
		return nil, failf("release manifest %s is too large", storePath)
	}
	zr, err := zstd.NewReader(io.LimitReader(o.Body, maxManifestNAR), zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	nar, err := io.ReadAll(io.LimitReader(zr, maxManifestNAR+1))
	if err != nil {
		return nil, fmt.Errorf("release manifest %s: %w", storePath, err)
	}
	if len(nar) > maxManifestNAR {
		return nil, failf("release manifest %s is too large", storePath)
	}
	var off, size uint64
	found := false
	err = cache.WalkNAR(bytes.NewReader(nar), func(e cache.NarEntry) error {
		if e.Path == "/release.json" && e.Type == "regular" {
			off, size, found = e.Offset, e.Size, true
		}
		return nil
	})
	if err != nil {
		return nil, failf("release manifest %s: %v", storePath, err)
	}
	if !found || off+size > uint64(len(nar)) || size > maxManifestJSON {
		return nil, failf("release output %s has no release.json", storePath)
	}
	return nar[off : off+size], nil
}
