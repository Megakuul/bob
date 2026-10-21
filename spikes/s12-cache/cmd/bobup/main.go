// Command bobup is the worker side of S12: after a build it uploads the bob-built part of a
// closure (and optionally a tiny OCI image) to staging/<build>/ in the artifacts bucket with
// worker credentials. It never signs. COMPLETE.json is written last; without it the
// orchestrator refuses to promote.
//
//	bobup -build ID [-image repo:tag=/nix/store/...] [-upstream https://cache.nixos.org] PATH...
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"bobspike/s12/nixfmt"
	"bobspike/s12/store"

	"github.com/klauspost/compress/zstd"
)

type pathInfo struct {
	NarHash    string   `json:"narHash"`
	NarSize    uint64   `json:"narSize"`
	References []string `json:"references"`
	Deriver    *string  `json:"deriver"`
	CA         *string  `json:"ca"`
	Ultimate   bool     `json:"ultimate"`
	Signatures []string `json:"signatures"`
}

// Complete is the staging manifest, written last.
type Complete struct {
	Build   string        `json:"build"`
	Paths   []string      `json:"paths"`  // store paths staged (bob-built only)
	Images  []StagedImage `json:"images"` // OCI images staged
	Skipped []string      `json:"skipped"`
}

type StagedImage struct {
	Repo      string   `json:"repo"`
	Tag       string   `json:"tag"`
	Manifest  string   `json:"manifest"` // sha256:<hex>
	MediaType string   `json:"mediaType"`
	Blobs     []string `json:"blobs"` // sha256:<hex>, incl. config and manifest
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bobup:", err)
		os.Exit(1)
	}
}

func run() error {
	build := flag.String("build", "", "build id")
	endpoint := flag.String("s3", "http://127.0.0.1:21203", "S3 endpoint")
	ak := flag.String("ak", "workerkey", "access key")
	sk := flag.String("sk", "workersecret0123456789", "secret key")
	bucket := flag.String("bucket", "artifacts", "bucket")
	upstream := flag.String("upstream", "https://cache.nixos.org", "skip paths this cache already has (R-REG-005); empty = no check")
	image := flag.String("image", "", "repo:tag=/nix/store/path: also stage a one-layer OCI image of that closure")
	par := flag.Int("j", 4, "parallel NAR uploads")
	level := flag.Int("zstd", 3, "zstd level (1,3,7,11 mapped to klauspost presets)")
	stopAfter := flag.Int("crash-after", -1, "test: exit(3) after N NARs, before COMPLETE.json")
	flag.Parse()
	if *build == "" || (flag.NArg() == 0 && *image == "") {
		return errors.New("usage: bobup -build ID [-image repo:tag=path] PATH...")
	}
	ctx := context.Background()
	st := store.New(*endpoint, *ak, *sk, *bucket)
	t0 := time.Now()

	roots := flag.Args()
	var img *imageSpec
	if *image != "" {
		s, err := parseImageSpec(*image)
		if err != nil {
			return err
		}
		img = s
		roots = append(roots, s.path)
	}
	infos, err := closure(roots)
	if err != nil {
		return err
	}
	var paths []string
	for p := range infos {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// R-REG-005: only bob-built outputs. Skip what upstream already serves.
	comp := Complete{Build: *build}
	var upload []string
	if *upstream != "" {
		have := upstreamHas(ctx, *upstream, paths)
		for _, p := range paths {
			if have[p] {
				comp.Skipped = append(comp.Skipped, p)
			} else {
				upload = append(upload, p)
			}
		}
	} else {
		upload = paths
	}
	fmt.Fprintf(os.Stderr, "closure %d paths, %d skipped (upstream), %d to stage\n", len(paths), len(comp.Skipped), len(upload))

	enc := zstd.EncoderLevelFromZstd(*level)
	var mu sync.Mutex
	var firstErr error
	var narBytes, fileBytes uint64
	sem := make(chan struct{}, *par)
	var wg sync.WaitGroup
	done := 0
	for _, p := range upload {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			ns, fs, err := stagePath(ctx, st, *build, p, infos[p], enc)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p, err)
			}
			narBytes += ns
			fileBytes += fs
			done++
			if *stopAfter >= 0 && done >= *stopAfter {
				fmt.Fprintf(os.Stderr, "crash-after: exiting after %d NARs without COMPLETE.json\n", done)
				os.Exit(3)
			}
		}(p)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	comp.Paths = upload
	tNix := time.Since(t0)

	if img != nil {
		si, err := stageImage(ctx, st, *build, img, infos)
		if err != nil {
			return fmt.Errorf("image: %w", err)
		}
		comp.Images = append(comp.Images, *si)
	}
	b, _ := json.MarshalIndent(comp, "", " ")
	if err := st.PutSmall(ctx, store.StagingComplete(*build), b, "application/json", false); err != nil {
		return err
	}
	el := time.Since(t0)
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	fmt.Printf("staged build=%s paths=%d nar=%.1fMiB zst=%.1fMiB nix=%.2fs total=%.2fs nar-throughput=%.1fMiB/s maxrss=%dMiB\n",
		*build, len(upload), mib(narBytes), mib(fileBytes), tNix.Seconds(), el.Seconds(), mib(narBytes)/tNix.Seconds(), ru.Maxrss/1024)
	return nil
}

func mib(n uint64) float64 { return float64(n) / (1 << 20) }

func closure(roots []string) (map[string]pathInfo, error) {
	args := append([]string{"path-info", "--json", "--json-format", "1", "-r"}, roots...)
	out, err := exec.Command("nix", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("nix path-info: %w", err)
	}
	m := map[string]pathInfo{}
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func upstreamHas(ctx context.Context, up string, paths []string) map[string]bool {
	have := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			req, _ := http.NewRequestWithContext(ctx, http.MethodHead, up+"/"+nixfmt.HashPart(p)+".narinfo", nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					mu.Lock()
					have[p] = true
					mu.Unlock()
				}
			}
		}(p)
	}
	wg.Wait()
	return have
}

type countHash struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n uint64
}

func (c *countHash) Write(p []byte) (int, error) { c.n += uint64(len(p)); return c.h.Write(p) }

// stagePath: nix-store --dump | sha256(nar) | zstd | sha256(file) | S3 multipart.
// Memory is bounded by the uploader's part buffers, never by the NAR size.
func stagePath(ctx context.Context, st *store.Store, build, p string, pi pathInfo, lvl zstd.EncoderLevel) (uint64, uint64, error) {
	h := nixfmt.HashPart(p)
	cmd := exec.CommandContext(ctx, "nix-store", "--dump", p)
	narOut, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}
	narH := &countHash{h: sha256.New()}
	fileH := &countHash{h: sha256.New()}
	pr, pw := io.Pipe()
	go func() {
		zw, err := zstd.NewWriter(io.MultiWriter(pw, fileH), zstd.WithEncoderLevel(lvl),
			zstd.WithEncoderConcurrency(runtime.GOMAXPROCS(0)))
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(zw, io.TeeReader(narOut, narH)); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(zw.Close())
	}()
	if err := st.Upload(ctx, store.StagingNar(build, h), pr, "application/zstd"); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return 0, 0, err
	}
	if err := cmd.Wait(); err != nil {
		return 0, 0, fmt.Errorf("nix-store --dump: %w", err)
	}
	narHash := "sha256:" + nixfmt.Base32(narH.h.Sum(nil))
	want, err := nixfmt.SRIToNix(pi.NarHash)
	if err != nil {
		return 0, 0, err
	}
	if narHash != want || narH.n != pi.NarSize {
		return 0, 0, fmt.Errorf("dumped NAR does not match the store's NarHash (%s != %s)", narHash, want)
	}
	ni := nixfmt.NarInfo{
		StorePath: p, URL: "nar/" + h + ".nar.zst", Compression: "zstd",
		FileHash: "sha256:" + nixfmt.Base32(fileH.h.Sum(nil)), FileSize: fileH.n,
		NarHash: narHash, NarSize: narH.n, References: pi.References,
	}
	if pi.Deriver != nil {
		ni.Deriver = filepath.Base(*pi.Deriver)
	}
	if pi.CA != nil {
		ni.CA = *pi.CA
	}
	// Unsigned claim; the orchestrator re-verifies every field it can.
	err = st.PutSmall(ctx, store.StagingNarInfo(build, h), []byte(ni.Format()), "text/x-nix-narinfo", false)
	return narH.n, fileH.n, err
}

type imageSpec struct{ repo, tag, path string }

func parseImageSpec(s string) (*imageSpec, error) {
	rt, p, ok := strings.Cut(s, "=")
	if !ok {
		return nil, errors.New("-image wants repo:tag=/nix/store/...")
	}
	i := strings.LastIndex(rt, ":")
	if i < 0 {
		return nil, errors.New("-image wants repo:tag")
	}
	return &imageSpec{repo: rt[:i], tag: rt[i+1:], path: p}, nil
}

// stageImage builds a deterministic one-layer image: the closure under /nix/store plus
// Entrypoint = <path>/bin/<first file>. S8 does real image building; this only feeds the
// registry test.
func stageImage(ctx context.Context, st *store.Store, build string, sp *imageSpec, infos map[string]pathInfo) (*StagedImage, error) {
	tmp, err := os.CreateTemp(os.Getenv("S12_TMP"), "layer-*.tar.gz")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	diffH := sha256.New()
	blobH := &countHash{h: sha256.New()}
	gz, _ := gzip.NewWriterLevel(io.MultiWriter(tmp, blobH), gzip.BestSpeed)
	tw := tar.NewWriter(io.MultiWriter(gz, diffH))
	var all []string
	for p := range infos {
		if strings.HasPrefix(p, "/nix/store/") {
			all = append(all, p)
		}
	}
	sort.Strings(all)
	epoch := time.Unix(1, 0)
	for _, d := range []string{"nix/", "nix/store/"} {
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d, Mode: 0o555, ModTime: epoch})
	}
	for _, root := range all {
		err := filepath.Walk(root, func(fp string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			name := strings.TrimPrefix(fp, "/")
			hdr := &tar.Header{Name: name, ModTime: epoch, Uname: "root", Gname: "root"}
			mode := int64(0o444)
			if fi.Mode()&0o111 != 0 {
				mode = 0o555
			}
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				t, err := os.Readlink(fp)
				if err != nil {
					return err
				}
				hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, t, 0o777
			case fi.IsDir():
				hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, name+"/", 0o555
			default:
				hdr.Typeflag, hdr.Size, hdr.Mode = tar.TypeReg, fi.Size(), mode
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if hdr.Typeflag == tar.TypeReg {
				f, err := os.Open(fp)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(tw, f)
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	layerHex := hex.EncodeToString(blobH.h.Sum(nil))
	diffID := "sha256:" + hex.EncodeToString(diffH.Sum(nil))
	if _, err := tmp.Seek(0, 0); err != nil {
		return nil, err
	}
	if err := st.Upload(ctx, store.StagingBlob(build, layerHex), tmp, "application/vnd.oci.image.layer.v1.tar+gzip"); err != nil {
		return nil, err
	}
	entry := sp.path
	if ents, err := os.ReadDir(filepath.Join(sp.path, "bin")); err == nil && len(ents) > 0 {
		entry = filepath.Join(sp.path, "bin", ents[0].Name())
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "amd64"}[runtime.GOARCH]
	cfg, _ := json.Marshal(map[string]any{
		"architecture": arch, "os": "linux",
		"config":  map[string]any{"Entrypoint": []string{entry}, "Env": []string{"PATH=" + filepath.Join(sp.path, "bin")}},
		"rootfs":  map[string]any{"type": "layers", "diff_ids": []string{diffID}},
		"history": []map[string]any{{"created_by": "bob (s12 spike)"}},
	})
	cfgHex := sha(cfg)
	if err := st.PutSmall(ctx, store.StagingBlob(build, cfgHex), cfg, "application/vnd.oci.image.config.v1+json", false); err != nil {
		return nil, err
	}
	man, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + cfgHex, "size": len(cfg)},
		"layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + layerHex, "size": blobH.n}},
	})
	manHex := sha(man)
	if err := st.PutSmall(ctx, store.StagingBlob(build, manHex), man, "application/vnd.oci.image.manifest.v1+json", false); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "image %s:%s layer=%.1fMiB manifest=sha256:%s\n", sp.repo, sp.tag, mib(blobH.n), manHex)
	return &StagedImage{Repo: sp.repo, Tag: sp.tag, Manifest: "sha256:" + manHex,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Blobs:     []string{"sha256:" + layerHex, "sha256:" + cfgHex, "sha256:" + manHex}}, nil
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
