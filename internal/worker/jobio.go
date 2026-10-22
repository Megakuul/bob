package worker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleet"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/engine/nar"
)

// Artifacts is the worker's view of the artifacts bucket (cache.Objects): it reads source
// snapshots and writes staging/ only (R-SEC-002).
type Artifacts interface {
	Get(ctx context.Context, key, rng, ifMatch string) (*cache.Object, error)
	Put(ctx context.Context, key string, body []byte, contentType string, c cache.Cond) (string, error)
	PutFile(ctx context.Context, key string, r io.ReadSeeker, size int64, contentType string) error
}

// UseArtifacts gives the job execution the artifacts bucket (source snapshots, staging) and
// the public cache whose paths are not staged; call it before Run.
func (p *Process) UseArtifacts(a Artifacts, upstream string) {
	p.cfg.Artifacts, p.cfg.Upstream = a, strings.TrimRight(upstream, "/")
}

// Source snapshot caps (SPEC §6.9.7).
const (
	maxSnapshot        = 512 << 20
	maxSnapshotEntries = 200_000
	maxSnapshotFile    = 4 << 30
)

// ErrUnsafePath is a source snapshot entry outside the repository (R-PIPE-009).
var ErrUnsafePath = errors.New("source snapshot contains an unsafe path")

// jobFailed is a job failure that is the job's own (not infrastructure).
type jobFailed struct{ msg string }

func (e *jobFailed) Error() string { return e.msg }

// prepareSource puts the job's source into <work>/src outside the container: the uploaded
// snapshot of a remote build, or a shallow git fetch with the checkout credential of the held
// job (§6.9.6; the credential never enters the container or .git/config).
func (p *Process) prepareSource(ctx context.Context, c Claim, spec store.JobSpec, work string) error {
	sel, err := jobspec.ParseSelection(spec.Targets)
	if err != nil {
		return &jobFailed{err.Error()}
	}
	dst := filepath.Join(work, workSrc)
	if sel.Upload != "" {
		return unpackSnapshot(ctx, p.cfg.Artifacts, jobspec.UploadKey(spec.Project, sel.Upload), dst)
	}
	co, err := p.checkout(ctx, c)
	if err != nil {
		return err
	}
	return gitFetch(ctx, dst, co, spec.Commit)
}

// checkout calls GetCheckoutCredential on an orchestrator and opens the sealed credential.
func (p *Process) checkout(ctx context.Context, c Claim) (fleet.CheckoutPlain, error) {
	var zero fleet.CheckoutPlain
	tok := p.token()
	if tok == nil || p.id == nil {
		return zero, errors.New("checkout: the worker has no token yet")
	}
	var last error
	for _, base := range p.attachAddrs(ctx) {
		base = strings.TrimRight(base, "/")
		cl := workerv1connect.NewWorkerControlServiceClient(attachClient(base), base+"/api")
		req := connect.NewRequest(&workerv1.GetCheckoutCredentialRequest{JobId: c.JobID, Epoch: c.Epoch})
		req.Header().Set("Authorization", tok.Authorization())
		req.Header().Set("Bob-PoP", tok.PoP(p.id, http.MethodPost, "/api/"+workerv1connect.WorkerControlServiceGetCheckoutCredentialProcedure, p.cfg.Now()))
		res, err := cl.GetCheckoutCredential(ctx, req)
		if err != nil {
			last = err
			continue
		}
		return fleet.OpenCheckout(p.id.kem, p.id.ID(), c.JobID, c.Epoch, res.Msg.GetSealedCheckout())
	}
	if last == nil {
		last = errors.New("no orchestrator address")
	}
	return zero, fmt.Errorf("checkout credential: %w", last)
}

// gitFetch fetches exactly one commit into dst. Refs and SHAs reach git only as separate,
// validated arguments (R-PIPE-008).
func gitFetch(ctx context.Context, dst string, co fleet.CheckoutPlain, commit string) error {
	if !strings.HasPrefix(co.CloneURL, "https://") {
		return fmt.Errorf("checkout: clone URL must be https")
	}
	want := commit
	if want == "" {
		want = co.SHA
	}
	what := want
	if co.FetchRef != "" && !isSHA(want) {
		what = co.FetchRef
	}
	if what == "" || strings.HasPrefix(what, "-") || strings.ContainsAny(what, " \t\n\x00") {
		return &jobFailed{fmt.Sprintf("checkout: invalid revision %q", what)}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(co.Username+":"+co.Token))
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dst}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dst, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.ReplaceAll(firstLineOf(string(out)), co.Token, "[redacted]"))
		}
		return out, nil
	}
	if _, err := git("init", "-q"); err != nil {
		return err
	}
	if _, err := git("-c", "http.extraHeader="+auth, "fetch", "-q", "--depth=1", "--", co.CloneURL, what); err != nil {
		return err
	}
	if _, err := git("checkout", "-q", "FETCH_HEAD"); err != nil {
		return err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if isSHA(want) && strings.TrimSpace(string(head)) != want {
		return &jobFailed{fmt.Sprintf("checkout: fetched %s, expected %s", strings.TrimSpace(string(head)), want)}
	}
	return os.RemoveAll(filepath.Join(dst, ".git"))
}

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// unpackSnapshot extracts src/<project>/<sha>.tar.zst into dst: regular files, directories
// and symlinks only, no absolute or ".." paths or link targets, bounded (§6.9.7).
func unpackSnapshot(ctx context.Context, a Artifacts, key, dst string) error {
	if a == nil {
		return errors.New("source snapshot: no artifacts bucket")
	}
	obj, err := a.Get(ctx, key, "", "")
	if err != nil {
		return fmt.Errorf("source snapshot %s: %w", key, err)
	}
	defer obj.Body.Close()
	zr, err := zstd.NewReader(io.LimitReader(obj.Body, maxSnapshot+1), zstd.WithDecoderMaxMemory(1<<30))
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return &jobFailed{"source snapshot unreadable: " + err.Error()}
		}
		if n >= maxSnapshotEntries {
			return &jobFailed{fmt.Sprintf("source snapshot has more than %d entries", maxSnapshotEntries)}
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if !safeRel(name) {
			return &jobFailed{fmt.Sprintf("%v: %s", ErrUnsafePath, h.Name)}
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if h.Size > maxSnapshotFile {
				return &jobFailed{"source snapshot file too large: " + name}
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return &jobFailed{"source snapshot: " + err.Error()}
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			target := path.Clean(path.Join(path.Dir(name), h.Linkname))
			if path.IsAbs(h.Linkname) || !safeRel(target) {
				return &jobFailed{fmt.Sprintf("%v: %s -> %s", ErrUnsafePath, h.Name, h.Linkname)}
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, p); err != nil {
				return &jobFailed{"source snapshot: " + err.Error()}
			}
		default:
			return &jobFailed{fmt.Sprintf("source snapshot entry %s has an unsupported type", name)}
		}
	}
}

func safeRel(name string) bool {
	return name != "" && name != "." && !path.IsAbs(name) && name != ".." && !strings.HasPrefix(name, "../")
}

// maxFailed and maxLogLast bound what goes into a JobResult (fleet.Decode refuses arrays of
// more than 1,024 elements and maps of more than 256 pairs).
const (
	maxFailed  = 1024
	maxLogLast = 256
)

// stage uploads what the runner left in <work>/out (SPEC §6.6.4 layout, outside the container,
// with the worker's credentials): the evaluation report, the NARs and narinfo claims of the
// closure paths that cache.nixos.org does not serve, and COMPLETE.json last. It fills the
// outcome's staging fields; a staging failure is infrastructure.
func (p *Process) stage(ctx context.Context, c Claim, spec store.JobSpec, work string, o *Outcome) {
	if len(o.LogLast) > maxLogLast {
		keep := map[string]int64{}
		for k, v := range o.LogLast {
			if len(keep) < maxLogLast {
				keep[k] = v
			}
		}
		o.LogLast = keep
	}
	if p.cfg.Artifacts == nil {
		return
	}
	staging := jobspec.StagingPrefix(spec.Staging, c.Attempt)
	outDir := filepath.Join(work, workOut)
	if rep, err := os.ReadFile(filepath.Join(outDir, reportFile)); err == nil && len(rep) <= jobspec.MaxReport {
		if _, err := p.cfg.Artifacts.Put(ctx, jobspec.ReportKey(staging), rep, "application/json", cache.Cond{}); err != nil {
			*o = Outcome{Status: "infra", Error: "staging the evaluation report: " + err.Error(), LogLast: o.LogLast}
			return
		}
		o.EvalSHA = jobspec.SHA256(rep)
	}
	var res RunResult
	if b, err := readCapped(filepath.Join(outDir, resultFile), maxResult); err == nil {
		if err := json.Unmarshal(b, &res); err != nil {
			*o = Outcome{Status: "infra", Error: "runner result unreadable: " + err.Error(), LogLast: o.LogLast}
			return
		}
	}
	o.Failed = res.Failed
	if len(o.Failed) > maxFailed {
		o.Failed = o.Failed[:maxFailed]
	}
	if o.Status != "ok" {
		return
	}
	done := promote.Complete{V: 1, Job: c.JobID, Build: spec.BuildID, Project: spec.Project,
		Paths: []string{}, Images: []promote.Image{}, Assets: []promote.Asset{}, Skipped: []string{}}
	if err := p.stagePaths(ctx, staging, work, res.Paths, &done); err != nil {
		*o = Outcome{Status: "infra", Error: "staging: " + err.Error(), LogLast: o.LogLast, EvalSHA: o.EvalSHA}
		return
	}
	body, err := json.Marshal(done)
	if err == nil {
		_, err = p.cfg.Artifacts.Put(ctx, staging+"COMPLETE.json", body, "application/json", cache.Cond{})
	}
	if err != nil {
		*o = Outcome{Status: "infra", Error: "staging COMPLETE.json: " + err.Error(), LogLast: o.LogLast, EvalSHA: o.EvalSHA}
		return
	}
	o.Complete, o.CompSHA = staging+"COMPLETE.json", jobspec.SHA256(body)
}

func readCapped(name string, max int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		err = fmt.Errorf("%s larger than %d bytes", name, max)
	}
	return b, err
}

// stagePaths stages the closure: a path cache.nixos.org serves is only listed as skipped
// (R-REG-005); every other path gets its zstd NAR and its narinfo claim.
func (p *Process) stagePaths(ctx context.Context, staging, work string, paths []PathInfo, done *promote.Complete) error {
	if len(paths) > maxStagedPaths {
		return fmt.Errorf("closure of %d paths, the limit is %d", len(paths), maxStagedPaths)
	}
	upstream := p.upstream(ctx, paths)
	sem := make(chan struct{}, 4)
	var runs []<-chan error
	for _, pi := range paths {
		if !cache.ValidStorePath(pi.Path) {
			return fmt.Errorf("closure path %q", pi.Path)
		}
		if upstream[pi.Path] {
			done.Skipped = append(done.Skipped, pi.Path)
			continue
		}
		done.Paths = append(done.Paths, pi.Path)
		sem <- struct{}{}
		runs = append(runs, safe.Go(func() error {
			defer func() { <-sem }()
			return p.stagePath(ctx, staging, work, pi)
		}))
	}
	var errs []error
	for _, r := range runs {
		if err := <-r; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stagePath writes <staging>nar/<hash>.nar.zst and then <staging>narinfo/<hash>.narinfo.
func (p *Process) stagePath(ctx context.Context, staging, work string, pi PathInfo) error {
	hash := cache.HashOf(pi.Path)
	host := filepath.Join(p.store, "store", strings.TrimPrefix(pi.Path, cache.StoreDir+"/"))
	tmp, err := os.CreateTemp(work, "nar-*.zst")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	fh := sha256.New()
	zw, err := zstd.NewWriter(io.MultiWriter(tmp, fh))
	if err != nil {
		return err
	}
	if err := nar.Write(zw, host, nil); err != nil {
		_ = zw.Close()
		return fmt.Errorf("NAR of %s: %w", pi.Path, err)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if err := p.cfg.Artifacts.PutFile(ctx, staging+"nar/"+hash+".nar.zst", tmp, size, "application/zstd"); err != nil {
		return fmt.Errorf("upload NAR of %s: %w", pi.Path, err)
	}
	ni := cache.NarInfo{StorePath: pi.Path, URL: "nar/" + hash + ".nar.zst", Compression: "zstd",
		FileHash: "sha256:" + cache.Nix32(fh.Sum(nil)), FileSize: uint64(size), NarHash: pi.NarHash, NarSize: pi.NarSize,
		References: pi.References}
	if _, err := p.cfg.Artifacts.Put(ctx, staging+"narinfo/"+hash+".narinfo", ni.Format(), "text/x-nix-narinfo", cache.Cond{}); err != nil {
		return fmt.Errorf("upload claim of %s: %w", pi.Path, err)
	}
	return nil
}

// upstream reports the paths cache.nixos.org serves (HEAD narinfo, 8 in parallel). Without
// an upstream, or when it cannot be reached, nothing is skipped: the path is staged and the
// orchestrator verifies it like any other.
func (p *Process) upstream(ctx context.Context, paths []PathInfo) map[string]bool {
	out := map[string]bool{}
	if p.cfg.Upstream == "" {
		return out
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	var runs []<-chan error
	for _, pi := range paths {
		sem <- struct{}{}
		runs = append(runs, safe.Go(func() error {
			defer func() { <-sem }()
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.cfg.Upstream+"/"+cache.HashOf(pi.Path)+".narinfo", nil)
			if err != nil {
				return nil
			}
			res, err := hc.Do(req)
			if err != nil {
				return nil
			}
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				mu.Lock()
				out[pi.Path] = true
				mu.Unlock()
			}
			return nil
		}))
	}
	for _, r := range runs {
		<-r
	}
	return out
}
