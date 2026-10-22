package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/store"
)

// maxImageObjects bounds the objects one image may reference (index, manifests, configs,
// layers).
const maxImageObjects = 4096

// PromoteImages is the Images hook of the promotion (SPEC §6.6.4 step 4): for each staged
// image it verifies every blob's digest while streaming it, copies it to oci/blobs/sha256/
// (ETag guard, create-only) unless served, adds the job's project to its visibility document,
// and writes the build tag (tag = build id) into oci/repos/<repo>/index.json last. A staged
// image of another project's repository, a blob that does not hash to its digest, or a
// manifest that references an object outside the image fails the job's promotion.
func (g *Registry) PromoteImages(ctx context.Context, j *promote.Job) error {
	for _, im := range j.Complete.Images {
		if err := g.promoteImage(ctx, j, im); err != nil {
			return err
		}
	}
	return nil
}

func stagedBlob(j *promote.Job, digest string) string {
	return j.Staging + "oci/blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
}

func (g *Registry) promoteImage(ctx context.Context, j *promote.Job, im promote.Image) error {
	if !ValidRepo(im.Repo) || RepoProject(im.Repo) != j.Project {
		return promote.VerifyFailed("image repository %.300q is not a repository of project %s", im.Repo, j.Project)
	}
	set := map[string]bool{im.Manifest: true}
	for _, d := range im.Blobs {
		set[d] = true
	}
	if len(set) > maxImageObjects {
		return promote.VerifyFailed("image %s lists more than %d objects", im.Repo, maxImageObjects)
	}
	// The manifest graph must stay inside the image's objects (staged copies, hostile input).
	media, err := g.checkGraph(ctx, j, im.Manifest, set, 0)
	if err != nil {
		return err
	}
	digests := make([]string, 0, len(set))
	for d := range set {
		digests = append(digests, d)
	}
	slices.Sort(digests)
	for _, d := range digests {
		if err := g.copyBlob(ctx, j, d); err != nil {
			return err
		}
		if err := g.c.Vis.AddProject(ctx, strings.TrimPrefix(d, "sha256:"), j.Project); err != nil {
			return err
		}
	}
	return g.Repos.SetBuildTag(ctx, im.Repo, j.Build, im.Manifest, media, g.c.Now().Unix())
}

// checkGraph reads the staged manifest or index d and checks that everything it references
// is in set; it returns d's media type.
func (g *Registry) checkGraph(ctx context.Context, j *promote.Job, d string, set map[string]bool, depth int) (string, error) {
	if depth > 2 {
		return "", promote.VerifyFailed("image index nesting too deep at %s", d)
	}
	body, _, err := g.c.Objects.Read(ctx, stagedBlob(j, d), maxManifest)
	switch {
	case errors.Is(err, store.ErrObjectNotFound):
		return "", promote.VerifyFailed("staged OCI object %s is missing", d)
	case errors.Is(err, store.ErrTooLarge):
		return "", promote.VerifyFailed("staged OCI manifest %s is too large", d)
	case err != nil:
		return "", err
	}
	if sum := sha256.Sum256(body); "sha256:"+hex.EncodeToString(sum[:]) != d {
		return "", promote.VerifyFailed("staged OCI manifest does not match its digest %s", d)
	}
	var m struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
		Config *struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", promote.VerifyFailed("staged OCI manifest %s is not JSON", d)
	}
	ref := func(x string) error {
		if !ValidDigest(x) || !set[x] {
			return promote.VerifyFailed("OCI manifest %s references %.80q, which the image does not list", d, x)
		}
		return nil
	}
	for _, c := range m.Manifests {
		if err := ref(c.Digest); err != nil {
			return "", err
		}
		if _, err := g.checkGraph(ctx, j, c.Digest, set, depth+1); err != nil {
			return "", err
		}
	}
	if m.Config != nil {
		if err := ref(m.Config.Digest); err != nil {
			return "", err
		}
	}
	for _, l := range m.Layers {
		if err := ref(l.Digest); err != nil {
			return "", err
		}
	}
	return MediaTypeOf(body), nil
}

// copyBlob verifies the staged copy of d (it proves the job had the bytes, so its project may
// see them) and copies it to the served key unless it is there.
func (g *Registry) copyBlob(ctx context.Context, j *promote.Job, d string) error {
	src := stagedBlob(j, d)
	o, err := g.c.Objects.Get(ctx, src, "", "")
	if errors.Is(err, store.ErrObjectNotFound) {
		return promote.VerifyFailed("staged OCI blob %s is missing", d)
	}
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, o.Body)
	_ = o.Body.Close()
	if err != nil {
		return fmt.Errorf("registry: read %s: %w", src, err)
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != d {
		return promote.VerifyFailed("staged OCI blob does not match its digest %s", d)
	}
	if _, err := g.c.Objects.Head(ctx, BlobKey(d)); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrObjectNotFound) {
		return err
	}
	err = g.c.Objects.Copy(ctx, src, o.ETag, BlobKey(d), map[string]string{"bob-digest": d}, "")
	if errors.Is(err, store.ErrPrecondition) {
		// another orchestrator copied it (content-addressed), or the staged copy changed
		// after we hashed it: the next pass verifies again
		if _, herr := g.c.Objects.Head(ctx, BlobKey(d)); herr == nil {
			return nil
		}
	}
	return err
}
