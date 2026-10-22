// Package bop holds the bob-builder kind "release": it writes release.json, the release
// manifest bob.release/v1 (SPEC R-OUT-023), from the built artifacts.
package bop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
)

// Schema is the manifest format.
const Schema = "bob.release/v1"

// Args is the Args schema of kind "release".
type Args struct {
	Project   string     `json:"project"`
	Release   string     `json:"release"`
	Label     string     `json:"label"`
	Version   string     `json:"version"`
	Public    bool       `json:"public"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one artifact as the generator knows it; the builder adds what only the built
// output tells (file name, hash and size, index digest, default entry points).
type Artifact struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"` // closure | oci | appimage | tarball
	Platform    string            `json:"platform"`
	StorePath   string            `json:"storePath"`
	Entrypoints map[string]string `json:"entrypoints"` // closure; empty = every bin/* of the output
	File        string            `json:"file"`        // appimage, tarball: glob of the file in StorePath
	Repo        string            `json:"repo"`        // oci
	Tags        []string          `json:"tags"`        // oci
}

// manifest and entry are release.json; fields in key order (keys sorted, R-OUT-023).
type manifest struct {
	Artifacts []entry `json:"artifacts"`
	Label     string  `json:"label"`
	Project   string  `json:"project"`
	Public    bool    `json:"public"`
	Release   string  `json:"release"`
	Schema    string  `json:"schema"`
	Version   string  `json:"version"`
}

type entry struct {
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

// Kinds is the release kind.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{"release": {Run: releaseKind, NoFixup: true}}
}

func releaseKind(c *builder.Ctx, raw json.RawMessage) error {
	var a Args
	if err := builder.DecodeArgs(raw, &a); err != nil {
		return err
	}
	m := manifest{Artifacts: []entry{}, Label: a.Label, Project: a.Project, Public: a.Public, Release: a.Release,
		Schema: Schema, Version: a.Version}
	for _, art := range a.Artifacts {
		e, err := describe(c.StoreDir, a.Version, art)
		if err != nil {
			return fmt.Errorf("%s %s: %w", art.Kind, art.Name, err)
		}
		m.Artifacts = append(m.Artifacts, e)
	}
	sort.SliceStable(m.Artifacts, func(i, j int) bool {
		x, y := m.Artifacts[i], m.Artifacts[j]
		return x.Name < y.Name || x.Name == y.Name && x.Platform < y.Platform
	})
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return err
	}
	out := c.Outputs["out"]
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "release.json"), b.Bytes(), 0o444)
}

// local is where a store path is readable in the build.
func local(storeDir, p string) string { return filepath.Join(storeDir, strings.TrimPrefix(p, "/nix/store/")) }

func describe(storeDir, version string, a Artifact) (entry, error) {
	e := entry{Kind: a.Kind, Name: a.Name, Platform: a.Platform, StorePath: a.StorePath}
	dir := local(storeDir, a.StorePath)
	switch a.Kind {
	case "closure":
		e.Entrypoints = a.Entrypoints
		if len(e.Entrypoints) == 0 {
			ents, err := os.ReadDir(filepath.Join(dir, "bin"))
			if err != nil {
				return e, fmt.Errorf("no main program and no bin/: %w", err)
			}
			e.Entrypoints = map[string]string{}
			for _, d := range ents {
				e.Entrypoints[d.Name()] = "bin/" + d.Name()
			}
		}
	case "oci":
		b, err := os.ReadFile(filepath.Join(dir, "index.json"))
		if err != nil {
			return e, err
		}
		var idx struct {
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
		}
		if err := json.Unmarshal(b, &idx); err != nil || len(idx.Manifests) != 1 {
			return e, fmt.Errorf("index.json does not name one image index (%v)", err)
		}
		e.Index, e.Repo, e.Tags = idx.Manifests[0].Digest, a.Repo, a.Tags
	case "appimage", "tarball":
		m, err := filepath.Glob(filepath.Join(dir, a.File))
		if err != nil || len(m) != 1 {
			return e, fmt.Errorf("want exactly one %s in %s, found %d (%v)", a.File, a.StorePath, len(m), err)
		}
		sum, size, err := hashFile(m[0])
		if err != nil {
			return e, err
		}
		e.File, e.SHA256, e.Size = Versioned(filepath.Base(m[0]), a.Name, version), sum, size
	default:
		return e, builder.Invalidf("unknown artifact kind %q", a.Kind)
	}
	return e, nil
}

// Versioned is the published name of an artifact file: the version after the artifact name
// ("api-tar-linux-arm64.tar.gz" → "api-tar-1.0.1-linux-arm64.tar.gz"), or before the extension
// when the file does not start with the name. The package output keeps its unversioned name, so
// its bytes and store path do not depend on the version (R-OUT-022).
func Versioned(file, name, version string) string {
	if rest, ok := strings.CutPrefix(file, name+"-"); ok {
		return name + "-" + version + "-" + rest
	}
	for _, ext := range []string{".tar.gz", ".AppImage"} {
		if stem, ok := strings.CutSuffix(file, ext); ok {
			return stem + "-" + version + ext
		}
	}
	return file + "-" + version
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
