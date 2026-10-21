// Package rootless is the rootless matrix runner (HARNESS.md §7.1). It runs a given bob binary
// as an unprivileged user inside pinned distro root filesystems, entered through user
// namespaces by the enter helper (tests/rootless/enter), and checks that the store works or
// that bob refuses with the exact fix text of SPEC R-NIX-022 when user namespaces are blocked.
//
// A rootfs changes userland only (libc, /etc, no /nix, CA files), never the kernel or its LSM
// policy. The real-host rows of HARNESS.md §7.2 stay manual.
package rootless

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Image is one pinned distro rootfs: an arm64 image manifest of an official Docker Hub image.
// The digests are the ones S8 §3.5 ran against (docs/spikes/s08-bundles.md).
type Image struct {
	Name   string // directory and subtest name
	Repo   string // Docker Hub repository
	Digest string // sha256 of the platform image manifest
	OSID   string // ID= in /etc/os-release, checked by the sandbox self-test
}

// Images is the matrix of HARNESS.md §7.1.
var Images = []Image{
	{"ubuntu-22.04", "library/ubuntu", "sha256:88881eb36da489f9ff8746ece8048ca16cc94f80e8cd3ae7e8335d8822631591", "ubuntu"},
	{"ubuntu-24.04", "library/ubuntu", "sha256:18b33ff3829707f31ada654a963b9ee35f2e6f0241b964bd17a22a9b330cb63d", "ubuntu"},
	{"debian-12", "library/debian", "sha256:441888b788d52d35c32f28c10abb5c748f3427c7836f61612700d30afb4572cb", "debian"},
	{"fedora-43", "library/fedora", "sha256:8de9a064b0d0371271a1ba30980563cb92af15e3be2a94b56d2b19fa215a4a45", "fedora"},
	{"alpine-3.22", "library/alpine", "sha256:2e1a7aa4cbc4e9e5222bb4c24a839aa1a6170ea5492d644777ce7b178824e44f", "alpine"},
}

// Key names the extracted rootfs directory; a new digest gets a new directory.
func (im Image) Key() string { return im.Name + "-" + strings.TrimPrefix(im.Digest, "sha256:")[:12] }

const (
	registry  = "https://registry-1.docker.io"
	authURL   = "https://auth.docker.io/token"
	doneMark  = ".bob-rootfs-complete"
	manifestT = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
)

type manifest struct {
	MediaType string `json:"mediaType"`
	Layers    []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"layers"`
}

// Fetcher downloads image blobs into a content-addressed cache and extracts root filesystems.
type Fetcher struct {
	Blobs   string // blob cache: <Blobs>/sha256/<hex>; verified on every write
	Client  *http.Client
	ChownTo *[2]int // uid, gid for every extracted entry (set when the runner is root)
}

// Rootfs returns the extracted rootfs of im under dir, fetching and extracting it if needed.
func (f *Fetcher) Rootfs(ctx context.Context, im Image, dir string) (string, error) {
	dst := filepath.Join(dir, im.Key())
	if _, err := os.Stat(filepath.Join(dst, doneMark)); err == nil {
		return dst, nil
	}
	layers, err := f.layers(ctx, im)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", im.Name, err)
	}
	tmp := dst + ".tmp"
	if err := removeAll(tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	for _, l := range layers {
		if err := f.apply(l, tmp); err != nil {
			return "", fmt.Errorf("extract %s layer %s: %w", im.Name, filepath.Base(l), err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, doneMark), []byte(im.Digest+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := f.chown(tmp); err != nil {
		return "", err
	}
	if err := removeAll(dst); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// layers returns the local paths of the image's layer blobs in order, downloading missing ones.
func (f *Fetcher) layers(ctx context.Context, im Image) ([]string, error) {
	mpath, err := f.blob(ctx, im, im.Digest, true)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(mpath)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", im.Digest, err)
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("manifest %s (%s) has no layers; the digest must name a platform manifest, not an index", im.Digest, m.MediaType)
	}
	var out []string
	for _, l := range m.Layers {
		if !strings.Contains(l.MediaType, "gzip") {
			return nil, fmt.Errorf("layer %s: unsupported media type %s", l.Digest, l.MediaType)
		}
		p, err := f.blob(ctx, im, l.Digest, false)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// blob makes sure the blob with the given digest is in the cache and returns its path.
func (f *Fetcher) blob(ctx context.Context, im Image, digest string, isManifest bool) (string, error) {
	hexd, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexd) != 64 {
		return "", fmt.Errorf("bad digest %q", digest)
	}
	p := filepath.Join(f.Blobs, "sha256", hexd)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tok, err := f.token(ctx, im.Repo)
	if err != nil {
		return "", err
	}
	kind := "blobs"
	if isManifest {
		kind = "manifests"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v2/%s/%s/%s", registry, im.Repo, kind, digest), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if isManifest {
		req.Header.Set("Accept", manifestT)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s %s: %s", kind, digest, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), hexd+".part-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download %s: %w", digest, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != hexd {
		return "", fmt.Errorf("download %s: content has sha256:%s", digest, got)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	return p, os.Rename(tmp.Name(), p)
}

func (f *Fetcher) token(ctx context.Context, repo string) (string, error) {
	q := url.Values{"service": {"registry.docker.io"}, "scope": {"repository:" + repo + ":pull"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: %s", resp.Status)
	}
	var t struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	return t.Token, nil
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

// apply extracts one gzip layer onto root, honouring OCI whiteouts. Device nodes are skipped:
// an unprivileged user cannot create them, and the sandbox binds the host /dev anyway.
func (f *Fetcher) apply(layer, root string) error {
	fh, err := os.Open(layer)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel := path.Clean("/" + hdr.Name)[1:]
		if rel == "" {
			continue
		}
		parent, err := resolveIn(root, path.Dir(rel))
		if err != nil {
			return err
		}
		base := path.Base(rel)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
		target := filepath.Join(parent, base)
		switch {
		case base == ".wh..wh..opq":
			ents, _ := os.ReadDir(parent)
			for _, e := range ents {
				if err := removeAll(filepath.Join(parent, e.Name())); err != nil {
					return err
				}
			}
			continue
		case strings.HasPrefix(base, ".wh."):
			if err := removeAll(filepath.Join(parent, strings.TrimPrefix(base, ".wh."))); err != nil {
				return err
			}
			continue
		}
		mode := fs.FileMode(hdr.Mode) & fs.ModePerm
		switch hdr.Typeflag {
		case tar.TypeDir:
			if fi, err := os.Lstat(target); err == nil && !fi.IsDir() {
				if err := os.Remove(target); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			// Owner write stays so later layers and cleanup can change the tree.
			if err := os.Chmod(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := removeAll(target); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			if err := os.Chmod(target, mode|0o600); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := removeAll(target); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			src, err := resolveIn(root, path.Clean("/" + hdr.Linkname)[1:])
			if err != nil {
				return err
			}
			if err := removeAll(target); err != nil {
				return err
			}
			if err := os.Link(src, target); err != nil {
				return err
			}
		}
	}
}

// resolveIn returns the host path of rel inside root, following symlinks as if root were /,
// so that a layer entry below a symlinked directory (usrmerge: bin -> usr/bin) cannot escape.
func resolveIn(root, rel string) (string, error) {
	cur := ""
	parts := strings.Split(rel, "/")
	for hops := 0; len(parts) > 0; {
		p := parts[0]
		parts = parts[1:]
		switch p {
		case "", ".":
			continue
		case "..":
			cur = path.Dir("/" + cur)[1:]
			continue
		}
		next := path.Join(cur, p)
		fi, err := os.Lstat(filepath.Join(root, next))
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if hops++; hops > 40 {
			return "", fmt.Errorf("%s: too many symlinks", rel)
		}
		link, err := os.Readlink(filepath.Join(root, next))
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(link, "/") {
			cur = ""
		}
		parts = append(strings.Split(link, "/"), parts...)
	}
	return filepath.Join(root, cur), nil
}

func (f *Fetcher) chown(dir string) error {
	if f.ChownTo == nil {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, f.ChownTo[0], f.ChownTo[1])
	})
}

// removeAll is os.RemoveAll that first makes directories writable: overlayfs leaves a mode-0
// work directory behind, and image layers contain read-only directories.
func removeAll(p string) error {
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}
