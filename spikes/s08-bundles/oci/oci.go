// Package oci builds OCI images straight from store closures: no Docker, no daemon,
// reproducible bytes. Spike code.
package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bobspike/s08/closure"
)

const (
	MediaManifest = "application/vnd.oci.image.manifest.v1+json"
	MediaIndex    = "application/vnd.oci.image.index.v1+json"
	MediaConfig   = "application/vnd.oci.image.config.v1+json"
	MediaLayerGz  = "application/vnd.oci.image.layer.v1.tar+gzip"
	layerFormat   = "bob-oci-layer-v1" // bump when tar/gzip rules change
)

// Epoch is the fixed mtime of every entry (1 s, not 0: some tools treat 0 as "unset").
var Epoch = time.Unix(1, 0).UTC()

// File is an extra entry in the customisation layer (/etc/passwd, cert symlinks, …).
type File struct {
	Path string // absolute, e.g. /etc/passwd
	Mode int64
	Data []byte
	Link string // symlink target if set
	Dir  bool
}

// Image is the input of one per-platform image.
type Image struct {
	Arch       string   // GOARCH spelling: arm64, amd64
	Roots      []string // store paths; their closure is the image content
	Entrypoint []string
	Cmd        []string
	Env        []string
	User       string // "uid:gid"
	WorkingDir string
	Labels     map[string]string
	MaxLayers  int    // 0 → 100; 1 → everything in one layer
	CACerts    string // store path of a CA bundle package (optional)
	TZData     string // store path of tzdata (optional)
	Extra      []File
}

type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

// Layout is an OCI image layout directory (oci-layout, index.json, blobs/sha256).
// LayerCache maps a layer key to its descriptor so unchanged store paths are not re-tarred.
type Layout struct {
	Dir   string
	mu    sync.Mutex
	cache map[string]layerInfo
}

type layerInfo struct {
	Desc   Descriptor `json:"desc"`
	DiffID string     `json:"diffID"`
}

// Stats of one build, for the spike's measurements.
type Stats struct {
	Layers, Reused int
	LayerBytes     int64
}

func OpenLayout(dir string) (*Layout, error) {
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755); err != nil {
		return nil, err
	}
	l := &Layout{Dir: dir, cache: map[string]layerInfo{}}
	if b, err := os.ReadFile(filepath.Join(dir, "layer-cache.json")); err == nil {
		_ = json.Unmarshal(b, &l.cache)
	}
	return l, os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644)
}

func (l *Layout) saveCache() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, _ := json.MarshalIndent(l.cache, "", " ")
	return os.WriteFile(filepath.Join(l.Dir, "layer-cache.json"), b, 0o644)
}

func (l *Layout) putBlob(media string, b []byte) (Descriptor, error) {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	p := filepath.Join(l.Dir, "blobs", "sha256", h)
	if _, err := os.Stat(p); err != nil {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return Descriptor{}, err
		}
	}
	return Descriptor{MediaType: media, Digest: "sha256:" + h, Size: int64(len(b))}, nil
}

// planLayers orders paths by popularity (most shared first, then name) and gives the top
// max-1 paths their own layer; the rest share the last layer. Same idea as nixpkgs'
// streamLayeredImage / nix2container, written from their docs.
func planLayers(c *closure.Closure, max int) [][]string {
	if max <= 0 {
		max = 100
	}
	pop := c.Popularity()
	// Roots (the program itself) always get their own top layer: they change on every
	// release, so they must not share a layer with stable libraries.
	isRoot := map[string]bool{}
	for _, r := range c.Roots {
		isRoot[r] = true
	}
	var paths, roots []string
	for _, p := range c.Sorted() {
		if isRoot[p] {
			roots = append(roots, p)
		} else {
			paths = append(paths, p)
		}
	}
	max -= len(roots)
	if max < 1 {
		max = 1
	}
	sort.SliceStable(paths, func(i, j int) bool { return pop[paths[i]] > pop[paths[j]] })
	var out [][]string
	if max == 1 || len(paths) == 0 {
		sort.Strings(paths)
		if len(paths) > 0 {
			out = append(out, paths)
		}
		for _, r := range roots {
			out = append(out, []string{r})
		}
		return out
	}
	for i, p := range paths {
		if i < max-1 {
			out = append(out, []string{p})
			continue
		}
		if len(out) < max {
			out = append(out, nil)
		}
		out[max-1] = append(out[max-1], p)
	}
	for _, g := range out {
		sort.Strings(g)
	}
	for _, r := range roots {
		out = append(out, []string{r})
	}
	return out
}

type hashWriter struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (w *hashWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return w.h.Write(p) }

// layer writes a reproducible tar.gz of the given store paths plus extra files.
func (l *Layout) layer(paths []string, extra []File) (layerInfo, bool, error) {
	key := layerKey(paths, extra)
	l.mu.Lock()
	li, ok := l.cache[key]
	l.mu.Unlock()
	if ok {
		if _, err := os.Stat(filepath.Join(l.Dir, "blobs", "sha256", strings.TrimPrefix(li.Desc.Digest, "sha256:"))); err == nil {
			return li, true, nil
		}
	}
	tmp, err := os.CreateTemp(filepath.Join(l.Dir, "blobs"), "layer-*")
	if err != nil {
		return li, false, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	comp := &hashWriter{h: sha256.New()}
	gz, _ := gzip.NewWriterLevel(io.MultiWriter(tmp, comp), gzip.DefaultCompression)
	// gzip header: no name, no mtime, OS byte fixed by the stdlib (255 = unknown).
	raw := &hashWriter{h: sha256.New()}
	tw := tar.NewWriter(io.MultiWriter(gz, raw))
	dirs := map[string]bool{}
	mkdirs := func(rel string) error {
		parts := strings.Split(filepath.Dir(rel), "/")
		for i := range parts {
			d := strings.Join(parts[:i+1], "/")
			if d == "." || dirs[d] {
				continue
			}
			dirs[d] = true
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755, ModTime: Epoch}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, sp := range paths {
		err := filepath.WalkDir(sp, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(p, "/")
			if err := mkdirs(rel); err != nil {
				return err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			h := &tar.Header{Name: rel, ModTime: Epoch, Mode: int64(fi.Mode().Perm())}
			switch {
			case fi.Mode()&fs.ModeSymlink != 0:
				t, err := os.Readlink(p)
				if err != nil {
					return err
				}
				h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, t, 0o777
			case fi.IsDir():
				h.Typeflag, h.Name = tar.TypeDir, rel+"/"
				dirs[rel] = true
			case fi.Mode().IsRegular():
				h.Typeflag, h.Size = tar.TypeReg, fi.Size()
			default:
				return fmt.Errorf("%s: unsupported file type", p)
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if h.Typeflag == tar.TypeReg {
				f, err := os.Open(p)
				if err != nil {
					return err
				}
				_, err = io.Copy(tw, f)
				f.Close()
				return err
			}
			return nil
		})
		if err != nil {
			return li, false, err
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Path < extra[j].Path })
	for _, f := range extra {
		rel := strings.TrimPrefix(f.Path, "/")
		if err := mkdirs(rel); err != nil {
			return li, false, err
		}
		h := &tar.Header{Name: rel, ModTime: Epoch, Mode: f.Mode}
		switch {
		case f.Dir:
			h.Typeflag, h.Name = tar.TypeDir, rel+"/"
			dirs[rel] = true
		case f.Link != "":
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, f.Link, 0o777
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(f.Data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return li, false, err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(f.Data); err != nil {
				return li, false, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return li, false, err
	}
	if err := gz.Close(); err != nil {
		return li, false, err
	}
	if err := tmp.Close(); err != nil {
		return li, false, err
	}
	h := hex.EncodeToString(comp.h.Sum(nil))
	if err := os.Rename(tmp.Name(), filepath.Join(l.Dir, "blobs", "sha256", h)); err != nil {
		return li, false, err
	}
	li = layerInfo{Desc: Descriptor{MediaType: MediaLayerGz, Digest: "sha256:" + h, Size: comp.n},
		DiffID: "sha256:" + hex.EncodeToString(raw.h.Sum(nil))}
	l.mu.Lock()
	l.cache[key] = li
	l.mu.Unlock()
	return li, false, nil
}

// layerKey: store paths are immutable, so their names identify the content.
func layerKey(paths []string, extra []File) string {
	h := sha256.New()
	fmt.Fprintln(h, layerFormat)
	for _, p := range paths {
		fmt.Fprintln(h, p)
	}
	b, _ := json.Marshal(extra)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

type config struct {
	Architecture string       `json:"architecture"`
	OS           string       `json:"os"`
	Config       imageConfig  `json:"config"`
	RootFS       rootFS       `json:"rootfs"`
	History      []historyEnt `json:"history"`
}
type imageConfig struct {
	User         string              `json:"User,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
}
type rootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}
type historyEnt struct {
	Created   string `json:"created"`
	CreatedBy string `json:"created_by"`
	Comment   string `json:"comment,omitempty"`
}

type manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Build writes one per-platform image into the layout and returns its manifest descriptor.
func (l *Layout) Build(ctx context.Context, img Image) (Descriptor, Stats, error) {
	var st Stats
	roots := append([]string{}, img.Roots...)
	env := append([]string{}, img.Env...)
	extra := append([]File{{Path: "/tmp", Dir: true, Mode: 0o1777}}, img.Extra...)
	if img.CACerts != "" {
		roots = append(roots, img.CACerts)
		b := img.CACerts + "/etc/ssl/certs/ca-bundle.crt"
		env = append(env, "SSL_CERT_FILE="+b)
		extra = append(extra,
			File{Path: "/etc/ssl/certs/ca-certificates.crt", Link: b},
			File{Path: "/etc/ssl/certs/ca-bundle.crt", Link: b},
			File{Path: "/etc/pki/tls/certs/ca-bundle.crt", Link: b})
	}
	if img.TZData != "" {
		roots = append(roots, img.TZData)
		env = append(env, "TZDIR="+img.TZData+"/share/zoneinfo")
		extra = append(extra, File{Path: "/usr/share/zoneinfo", Link: img.TZData + "/share/zoneinfo"},
			File{Path: "/etc/localtime", Link: img.TZData + "/share/zoneinfo/UTC"})
	}
	if img.User != "" {
		uid, gid, _ := strings.Cut(img.User, ":")
		if gid == "" {
			gid = uid
		}
		extra = append(extra,
			File{Path: "/etc/passwd", Mode: 0o644, Data: []byte("root:x:0:0:root:/root:/noshell\napp:x:" + uid + ":" + gid + ":app:/tmp:/noshell\n")},
			File{Path: "/etc/group", Mode: 0o644, Data: []byte("root:x:0:\napp:x:" + gid + ":\n")},
			File{Path: "/etc/nsswitch.conf", Mode: 0o644, Data: []byte("passwd: files\ngroup: files\nhosts: files dns\n")})
	}
	c, err := closure.Query(ctx, roots...)
	if err != nil {
		return Descriptor{}, st, err
	}
	c.Roots = img.Roots // cert/tz packages are ordinary (stable) paths for layering
	groups := planLayers(c, img.MaxLayers-1) // last layer is the customisation layer
	if img.MaxLayers == 1 {
		groups = nil
	}
	type res struct {
		li     layerInfo
		reused bool
		err    error
	}
	results := make([]res, len(groups)+1)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[i].err = fmt.Errorf("layer %d: panic %v", i, r)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			if i < len(groups) {
				results[i].li, results[i].reused, results[i].err = l.layer(groups[i], nil)
			} else if img.MaxLayers == 1 {
				results[i].li, results[i].reused, results[i].err = l.layer(c.Sorted(), extra)
			} else {
				results[i].li, results[i].reused, results[i].err = l.layer(nil, extra)
			}
		}(i)
	}
	wg.Wait()
	cfg := config{Architecture: img.Arch, OS: "linux",
		Config: imageConfig{User: img.User, Env: env, Entrypoint: img.Entrypoint, Cmd: img.Cmd, WorkingDir: img.WorkingDir, Labels: img.Labels},
		RootFS: rootFS{Type: "layers"}}
	var mf manifest
	mf.SchemaVersion, mf.MediaType = 2, MediaManifest
	for i, r := range results {
		if r.err != nil {
			return Descriptor{}, st, r.err
		}
		st.Layers++
		st.LayerBytes += r.li.Desc.Size
		if r.reused {
			st.Reused++
		}
		cfg.RootFS.DiffIDs = append(cfg.RootFS.DiffIDs, r.li.DiffID)
		by := "bob: customisation"
		if i < len(groups) {
			by = "bob: " + strings.Join(baseNames(groups[i]), " ")
		}
		cfg.History = append(cfg.History, historyEnt{Created: Epoch.Format(time.RFC3339), CreatedBy: by})
		mf.Layers = append(mf.Layers, r.li.Desc)
	}
	cb, _ := json.Marshal(cfg)
	if mf.Config, err = l.putBlob(MediaConfig, cb); err != nil {
		return Descriptor{}, st, err
	}
	mb, _ := json.Marshal(mf)
	d, err := l.putBlob(MediaManifest, mb)
	if err != nil {
		return d, st, err
	}
	d.Platform = &Platform{Architecture: img.Arch, OS: "linux"}
	return d, st, l.saveCache()
}

func baseNames(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = filepath.Base(p)
	}
	if len(out) > 5 {
		out = append(out[:5], fmt.Sprintf("+%d more", len(ps)-5))
	}
	return out
}

// Index writes a multi-platform image index and points index.json (the layout's entry) at it.
func (l *Layout) Index(ref string, manifests ...Descriptor) (Descriptor, error) {
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Platform.Architecture < manifests[j].Platform.Architecture })
	idx := struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Manifests     []Descriptor `json:"manifests"`
	}{2, MediaIndex, manifests}
	b, _ := json.Marshal(idx)
	d, err := l.putBlob(MediaIndex, b)
	if err != nil {
		return d, err
	}
	top := d
	top.Annotations = map[string]string{"org.opencontainers.image.ref.name": ref}
	idx.Manifests = []Descriptor{top}
	b, _ = json.Marshal(idx)
	return d, os.WriteFile(filepath.Join(l.Dir, "index.json"), b, 0o644)
}
