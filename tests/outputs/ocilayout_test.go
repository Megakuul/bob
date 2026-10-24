package outputs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Types of an OCI image layout (https://github.com/opencontainers/image-spec).

type platformT struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *platformT        `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type indexT struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
}

type manifestT struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

type configT struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		User         string              `json:"User"`
		WorkingDir   string              `json:"WorkingDir"`
		Env          []string            `json:"Env"`
		Entrypoint   []string            `json:"Entrypoint"`
		Cmd          []string            `json:"Cmd"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		Labels       map[string]string   `json:"Labels"`
	} `json:"config"`
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
	History []struct {
		Created string `json:"created"`
	} `json:"history"`
}

// layout is an OCI image layout directory (the output of an oci.Image, R-OUT-011).
type layout struct {
	t   testing.TB
	dir string
}

func openLayout(t testing.TB, dir string) layout {
	t.Helper()
	l := layout{t: t, dir: dir}
	if got := strings.TrimSpace(string(readFile(t, filepath.Join(dir, "oci-layout")))); got != `{"imageLayoutVersion":"1.0.0"}` {
		t.Errorf("oci-layout is %q, want {\"imageLayoutVersion\":\"1.0.0\"}", got)
	}
	return l
}

// blob reads blobs/sha256/<hex>; symlinks to layer outputs are followed.
func (l layout) blob(digest string) []byte {
	l.t.Helper()
	return readFile(l.t, l.blobPath(digest))
}

func (l layout) blobPath(digest string) string {
	return filepath.Join(l.dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
}

func decode[T any](t testing.TB, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %T: %v\n%.300s", v, err, b)
	}
	return v
}

// top is index.json: it lists the one image index.
func (l layout) top() indexT {
	l.t.Helper()
	return decode[indexT](l.t, readFile(l.t, filepath.Join(l.dir, "index.json")))
}

// imageIndex returns the image index (the single entry of index.json) and its descriptor.
func (l layout) imageIndex() (indexT, descriptor) {
	l.t.Helper()
	top := l.top()
	if len(top.Manifests) != 1 {
		l.t.Fatalf("index.json lists %d manifests, want the one image index", len(top.Manifests))
	}
	d := top.Manifests[0]
	return decode[indexT](l.t, l.blob(d.Digest)), d
}

// manifestFor returns the image manifest, its descriptor and config for the architecture.
func (l layout) manifestFor(arch string) (manifestT, descriptor, configT) {
	l.t.Helper()
	idx, _ := l.imageIndex()
	for _, d := range idx.Manifests {
		if d.Platform != nil && d.Platform.Architecture == arch {
			m := decode[manifestT](l.t, l.blob(d.Digest))
			return m, d, decode[configT](l.t, l.blob(m.Config.Digest))
		}
	}
	l.t.Fatalf("image index has no manifest for %s", arch)
	return manifestT{}, descriptor{}, configT{}
}

// layerEntries reads layer i of a manifest.
func (l layout) layerEntries(m manifestT, i int) []tarEntry {
	l.t.Helper()
	return readTar(l.t, l.blob(m.Layers[i].Digest))
}

// storeTops lists the store paths (nix/store/<base>) that a layer contains.
func storeTops(es []tarEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range es {
		n := strings.TrimPrefix(strings.TrimPrefix(e.Hdr.Name, "./"), "/")
		rest, ok := strings.CutPrefix(n, "nix/store/")
		if !ok || rest == "" {
			continue
		}
		top := strings.SplitN(strings.TrimSuffix(rest, "/"), "/", 2)[0]
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
		}
	}
	return out
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// ---- read-only registry over a layout ----

// serveLayout serves the layout as an OCI distribution registry (GET/HEAD manifests and blobs)
// on a loopback port; the tag names the image index. crane validate --remote and podman pull talk
// to it without a push step. It returns host:port.
func serveLayout(t testing.TB, l layout, repo, tag string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	_, top := l.imageIndex()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/v2/")
		switch {
		case p == "":
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(p, repo+"/manifests/"):
			ref := strings.TrimPrefix(p, repo+"/manifests/")
			digest := top.Digest
			if ref != tag {
				digest = ref
			}
			b, err := os.ReadFile(l.blobPath(digest))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			var probe struct {
				MediaType string `json:"mediaType"`
			}
			_ = json.Unmarshal(b, &probe)
			w.Header().Set("Content-Type", probe.MediaType)
			w.Header().Set("Docker-Content-Digest", digestOf(b))
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
		case strings.HasPrefix(p, repo+"/blobs/"):
			f, err := os.Open(l.blobPath(strings.TrimPrefix(p, repo+"/blobs/")))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, "", time.Time{}, f)
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}
