// Package bop holds the bob-builder kinds of OCI images (SPEC §04.5): "oci-layer" writes one
// reproducible layer of store paths, "oci-image" writes the image layout. Neither runs a
// container tool, a daemon or anything over the network (R-OUT-006).
package bop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
)

// Media types of the OCI image specification.
const (
	MediaIndex    = "application/vnd.oci.image.index.v1+json"
	MediaManifest = "application/vnd.oci.image.manifest.v1+json"
	MediaConfig   = "application/vnd.oci.image.config.v1+json"
	MediaLayerGz  = "application/vnd.oci.image.layer.v1.tar+gzip"
)

// created is the time of every history entry (R-OUT-010).
const created = "1970-01-01T00:00:01Z"

// Kinds are the OCI kinds. Their outputs must stay byte-exact: no fixup.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{
		"oci-layer": {Run: layerKind, NoFixup: true},
		"oci-image": {Run: imageKind, NoFixup: true},
	}
}

func layerKind(c *builder.Ctx, raw json.RawMessage) error {
	var a LayerArgs
	if err := builder.DecodeArgs(raw, &a); err != nil {
		return err
	}
	if a.Format != LayerFormat {
		return builder.Invalidf("layer format %q, this builder writes %s", a.Format, LayerFormat)
	}
	if len(a.Paths) == 0 {
		return builder.Invalidf("a layer needs at least one store path")
	}
	out := c.Outputs["out"]
	skip := map[string]bool{}
	for _, s := range a.Skip {
		skip[s] = true
	}
	entries, err := storeEntries(c.StoreDir, a.Paths, skip)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	c.Phase("layer")
	res, err := builder.WriteTarGz(filepath.Join(out, "layer.tar.gz"), entries)
	if err != nil {
		return err
	}
	info, err := json.Marshal(LayerInfo{DiffID: res.DiffID, Digest: res.Digest, Size: res.Size, Paths: a.Paths})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "layer.json"), append(info, '\n'), 0o444)
}

// storeEntries lists nix/, nix/store/ and the trees of paths (read from storeDir) without the
// skipped entries.
func storeEntries(storeDir string, paths []string, skip map[string]bool) ([]builder.TarEntry, error) {
	entries := []builder.TarEntry{builder.TarDir("nix", 0o555), builder.TarDir("nix/store", 0o555)}
	for _, p := range paths {
		base := filepath.Base(p)
		if !strings.HasPrefix(p, "/nix/store/") || strings.Contains(strings.TrimPrefix(p, "/nix/store/"), "/") {
			return nil, builder.Invalidf("%q is not a store path", p)
		}
		es, err := builder.TreeEntries(filepath.Join(storeDir, base), "nix/store/"+base, func(rel string) bool {
			return rel != "" && skip[p+"/"+rel]
		})
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		entries = append(entries, es...)
	}
	return entries, nil
}

// Types of the OCI image layout, written with encoding/json in this field order (R-OUT-010).
type (
	descriptor struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Size        int64             `json:"size"`
		Platform    *platform         `json:"platform,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}
	platform struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	}
	index struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Manifests     []descriptor `json:"manifests"`
	}
	manifest struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	imageConfig struct {
		Architecture string      `json:"architecture"`
		OS           string      `json:"os"`
		Config       runConfig   `json:"config"`
		RootFS       rootFS      `json:"rootfs"`
		History      []historyEx `json:"history"`
	}
	runConfig struct {
		User         string              `json:"User,omitempty"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
		Env          []string            `json:"Env,omitempty"`
		Entrypoint   []string            `json:"Entrypoint,omitempty"`
		Cmd          []string            `json:"Cmd,omitempty"`
		WorkingDir   string              `json:"WorkingDir,omitempty"`
		Labels       map[string]string   `json:"Labels,omitempty"`
	}
	rootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	}
	historyEx struct {
		Created   string `json:"created"`
		CreatedBy string `json:"created_by"`
	}
)

// layout writes blobs of an OCI image layout.
type layout struct {
	dir string
}

// put writes b as a blob and returns its descriptor.
func (l layout) put(mediaType string, b []byte) (descriptor, error) {
	sum := sha256.Sum256(b)
	hx := hex.EncodeToString(sum[:])
	p := filepath.Join(l.dir, "blobs", "sha256", hx)
	if _, err := os.Lstat(p); err != nil {
		if err := os.WriteFile(p, b, 0o444); err != nil {
			return descriptor{}, err
		}
	}
	return descriptor{MediaType: mediaType, Digest: "sha256:" + hx, Size: int64(len(b))}, nil
}

// link adds the layer of a layer derivation output as a symlink blob (R-OUT-011); the layer
// output must be a store path next to the image's.
func (l layout) link(layerOut string) (descriptor, string, error) {
	b, err := os.ReadFile(filepath.Join(layerOut, "layer.json"))
	if err != nil {
		return descriptor{}, "", fmt.Errorf("layer %s: %w", layerOut, err)
	}
	var info LayerInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return descriptor{}, "", fmt.Errorf("layer %s: layer.json: %w", layerOut, err)
	}
	hx, ok := strings.CutPrefix(info.Digest, "sha256:")
	if !ok || len(hx) != 64 || info.Size <= 0 || !strings.HasPrefix(info.DiffID, "sha256:") {
		return descriptor{}, "", fmt.Errorf("layer %s: invalid layer.json", layerOut)
	}
	p := filepath.Join(l.dir, "blobs", "sha256", hx)
	if _, err := os.Lstat(p); err != nil {
		// Relative (store dir/<image>/blobs/sha256/<hex> → store dir/<layer>/layer.tar.gz), so
		// the link also resolves where the store is not mounted at /nix/store (the store below
		// bob's home on the host); Nix still records the layer as a reference (its hash is in
		// the target).
		if err := os.Symlink("../../../"+filepath.Base(layerOut)+"/layer.tar.gz", p); err != nil {
			return descriptor{}, "", err
		}
	}
	return descriptor{MediaType: MediaLayerGz, Digest: info.Digest, Size: info.Size}, info.DiffID, nil
}

// jsonBytes encodes v without HTML escaping.
func jsonBytes(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func imageKind(c *builder.Ctx, raw json.RawMessage) error {
	var a ImageArgs
	if err := builder.DecodeArgs(raw, &a); err != nil {
		return err
	}
	if a.Error != "" {
		return errors.New(a.Error)
	}
	if len(a.Manifests) == 0 {
		return builder.Invalidf("an image needs at least one platform")
	}
	l := layout{dir: c.Outputs["out"]}
	if err := os.MkdirAll(filepath.Join(l.dir, "blobs", "sha256"), 0o755); err != nil {
		return err
	}
	c.Phase("image")
	idx := index{SchemaVersion: 2, MediaType: MediaIndex, Manifests: []descriptor{}}
	for _, m := range a.Manifests {
		d, err := l.manifest(c, m)
		if err != nil {
			return err
		}
		d.Platform = &platform{Architecture: m.Architecture, OS: "linux"}
		idx.Manifests = append(idx.Manifests, d)
	}
	ib, err := jsonBytes(idx)
	if err != nil {
		return err
	}
	id, err := l.put(MediaIndex, ib)
	if err != nil {
		return err
	}
	if a.Tag != "" {
		id.Annotations = map[string]string{"org.opencontainers.image.ref.name": a.Tag}
	}
	top, err := jsonBytes(index{SchemaVersion: 2, MediaType: MediaIndex, Manifests: []descriptor{id}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(l.dir, "index.json"), top, 0o444); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o444)
}

// manifest writes the layers, configuration and manifest of one platform.
func (l layout) manifest(c *builder.Ctx, m Manifest) (descriptor, error) {
	man := manifest{SchemaVersion: 2, MediaType: MediaManifest, Layers: []descriptor{}}
	cfg := imageConfig{Architecture: m.Architecture, OS: "linux", RootFS: rootFS{Type: "layers", DiffIDs: []string{}}}
	for _, out := range m.Layers {
		d, diffID, err := l.link(out)
		if err != nil {
			return descriptor{}, err
		}
		man.Layers = append(man.Layers, d)
		cfg.RootFS.DiffIDs = append(cfg.RootFS.DiffIDs, diffID)
		cfg.History = append(cfg.History, historyEx{Created: created, CreatedBy: "bob: " + strings.Join(layerPaths(out), " ")})
	}
	var custom bytes.Buffer
	res, err := builder.TarGzTo(&custom, customEntries(m.Custom))
	if err != nil {
		return descriptor{}, err
	}
	cd, err := l.put(MediaLayerGz, custom.Bytes())
	if err != nil {
		return descriptor{}, err
	}
	man.Layers = append(man.Layers, cd)
	cfg.RootFS.DiffIDs = append(cfg.RootFS.DiffIDs, res.DiffID)
	cfg.History = append(cfg.History, historyEx{Created: created, CreatedBy: "bob: customisation"})
	rc := m.Config
	cfg.Config = runConfig{User: rc.User, Env: rc.Env, Entrypoint: rc.Entrypoint, Cmd: rc.Cmd, WorkingDir: rc.WorkingDir,
		Labels: rc.Labels}
	if len(rc.ExposedPorts) > 0 {
		cfg.Config.ExposedPorts = map[string]struct{}{}
		for _, p := range rc.ExposedPorts {
			cfg.Config.ExposedPorts[p] = struct{}{}
		}
	}
	cb, err := jsonBytes(cfg)
	if err != nil {
		return descriptor{}, err
	}
	if man.Config, err = l.put(MediaConfig, cb); err != nil {
		return descriptor{}, err
	}
	mb, err := jsonBytes(man)
	if err != nil {
		return descriptor{}, err
	}
	c.Logf("%s: %d layers", m.Architecture, len(man.Layers))
	return l.put(MediaManifest, mb)
}

// layerPaths are the store path names of a layer output (for the history), best effort.
func layerPaths(out string) []string {
	b, err := os.ReadFile(filepath.Join(out, "layer.json"))
	if err != nil {
		return nil
	}
	var info LayerInfo
	if json.Unmarshal(b, &info) != nil {
		return nil
	}
	names := make([]string, 0, len(info.Paths))
	for _, p := range info.Paths {
		names = append(names, filepath.Base(p))
	}
	sort.Strings(names)
	return names
}

// customEntries is the customisation layer (R-OUT-012).
func customEntries(cu Custom) []builder.TarEntry {
	es := []builder.TarEntry{
		builder.TarDir(".bob", 0o755),
		builder.TarFile(".bob/licences.json", 0o444, []byte(cu.Licences)),
		builder.TarDir("etc", 0o755),
		builder.TarFile("etc/group", 0o644, []byte("root:x:0:\napp:x:65534:\n")),
		builder.TarFile("etc/nsswitch.conf", 0o644, []byte("hosts: files dns\n")),
		builder.TarFile("etc/passwd", 0o644, []byte("root:x:0:0::/root:/sbin/nologin\napp:x:65534:65534::/tmp:/sbin/nologin\n")),
		builder.TarDir("nix", 0o555),
		builder.TarDir("nix/store", 0o555),
		builder.TarDir("tmp", 0o1777),
	}
	if cu.CACert != "" {
		es = append(es, builder.TarDir("etc/ssl", 0o755), builder.TarDir("etc/ssl/certs", 0o755),
			builder.TarSymlink("etc/ssl/certs/ca-certificates.crt", cu.CACert),
			builder.TarSymlink("etc/ssl/certs/ca-bundle.crt", cu.CACert))
	}
	if cu.ZoneInfo != "" {
		es = append(es, builder.TarDir("usr", 0o755), builder.TarDir("usr/share", 0o755),
			builder.TarSymlink("usr/share/zoneinfo", cu.ZoneInfo))
	}
	return slices.Clip(es)
}
