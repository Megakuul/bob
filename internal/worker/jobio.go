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
	"net/url"
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
	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
	"github.com/megakuul/bob/internal/gen/bob/auth/v1/authv1connect"
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
// job (§6.9.6; the credential never enters the container or .git/config). cred is the answer
// of GetCheckoutCredential (credErr when it failed), which only a git fetch needs.
func (p *Process) prepareSource(ctx context.Context, c Claim, spec store.JobSpec, work string, cred *jobCredential, credErr error) error {
	sel, err := jobspec.ParseSelection(spec.Targets)
	if err != nil {
		return &jobFailed{err.Error()}
	}
	dst := filepath.Join(work, workSrc)
	if sel.Upload != "" {
		return unpackSnapshot(ctx, p.cfg.Artifacts, jobspec.UploadKey(spec.Project, sel.Upload), dst)
	}
	if credErr != nil {
		return credErr
	}
	co, err := p.openCheckout(c, cred)
	if err != nil {
		return err
	}
	return gitFetch(ctx, dst, co, spec.Commit)
}

// openCheckout opens the sealed checkout credential of GetCheckoutCredential's answer.
func (p *Process) openCheckout(c Claim, cred *jobCredential) (fleet.CheckoutPlain, error) {
	co, err := fleet.OpenCheckout(p.id.kem, p.id.ID(), c.JobID, c.Epoch, cred.res.GetSealedCheckout())
	if err != nil {
		return fleet.CheckoutPlain{}, fmt.Errorf("checkout credential: %w", err)
	}
	return co, nil
}

// jobCredential is GetCheckoutCredential's answer for a held job and the orchestrator that
// gave it: its /nix is the job's bob cache (§6.6.1), reachable from this worker.
type jobCredential struct {
	base string
	res  *workerv1.GetCheckoutCredentialResponse
}

// credential calls GetCheckoutCredential on the orchestrators until one answers.
func (p *Process) credential(ctx context.Context, c Claim) (*jobCredential, error) {
	tok := p.token()
	if tok == nil || p.id == nil {
		return nil, errors.New("checkout: the worker has no token yet")
	}
	var last error
	for _, base := range p.attachAddrs(ctx) {
		base = strings.TrimRight(base, "/")
		cl := workerv1connect.NewWorkerControlServiceClient(attachClient(base), base+"/api")
		req := connect.NewRequest(&workerv1.GetCheckoutCredentialRequest{JobId: c.JobID, Epoch: c.Epoch})
		req.Header().Set("Authorization", tok.Authorization())
		req.Header().Set("Bob-PoP", tok.PoP(p.id, http.MethodPost, "/api"+workerv1connect.WorkerControlServiceGetCheckoutCredentialProcedure, p.cfg.Now()))
		res, err := cl.GetCheckoutCredential(ctx, req)
		if err != nil {
			last = err
			continue
		}
		return &jobCredential{base: base, res: res.Msg}, nil
	}
	if last == nil {
		last = errors.New("no orchestrator address")
	}
	return nil, fmt.Errorf("checkout credential: %w", last)
}

// jobCache is what the job's Nix needs to substitute from bob's cache: the substituter, the
// keys it signs with and the job's download token (netrc). The zero value is no bob cache.
type jobCache struct {
	url, host, token string
	keys             []string
}

// cacheOf asks the orchestrator that answered GetCheckoutCredential for its cache keys
// (GetServerInfo, public): a cache whose keys are unknown is not used (require-sigs would
// refuse its paths anyway). Without a download token only public paths substitute.
func cacheOf(ctx context.Context, cred *jobCredential) (jobCache, error) {
	if cred == nil {
		return jobCache{}, nil
	}
	u, err := url.Parse(cred.base)
	if err != nil || u.Hostname() == "" {
		return jobCache{}, fmt.Errorf("orchestrator address %q: no host", cred.base)
	}
	cl := authv1connect.NewAuthServiceClient(attachClient(cred.base), cred.base+"/api")
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := cl.GetServerInfo(ictx, connect.NewRequest(&authv1.GetServerInfoRequest{}))
	if err != nil {
		return jobCache{}, fmt.Errorf("cache keys: %w", err)
	}
	keys := info.Msg.GetNix().GetPublicKeys()
	if len(keys) == 0 {
		return jobCache{}, nil
	}
	for _, k := range keys {
		if k == "" || strings.ContainsAny(k, " \t\r\n") {
			return jobCache{}, fmt.Errorf("cache keys: malformed key %q", k)
		}
	}
	tok := cred.res.GetDownloadToken()
	if strings.ContainsAny(tok, " \t\r\n#") {
		return jobCache{}, errors.New("cache: malformed download token")
	}
	return jobCache{url: cred.base + "/nix", host: u.Hostname(), token: tok, keys: keys}, nil
}

// netrc is the job's netrc (SPEC §6.6.1: user bob, password = the download token); empty
// without a token.
func (jc jobCache) netrc() []byte {
	if jc.token == "" || jc.host == "" {
		return nil
	}
	return []byte("machine " + jc.host + " login bob password " + jc.token + "\n")
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
	// The credential reaches git through its environment (GIT_CONFIG_COUNT/KEY/VALUE, git
	// 2.31+), which only the worker's own uid can read, never through the command line that
	// every local process can read in /proc/<pid>/cmdline (I-R2 F7).
	withAuth := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + auth}
	var cmdEnv []string
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dst}, args...)...)
		cmd.Env = append(gitEnv(dst), cmdEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.ReplaceAll(firstLineOf(string(out)), co.Token, "[redacted]"))
		}
		return out, nil
	}
	if _, err := git("init", "-q"); err != nil {
		return err
	}
	cmdEnv = withAuth
	_, err := git("fetch", "-q", "--depth=1", "--", co.CloneURL, what)
	cmdEnv = nil
	if err != nil {
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

// gitEnv is the scrubbed environment of the checkout's git: no worker credentials, no system
// config. git trusts the CAs the worker's own TLS client trusts (SSL_CERT_FILE / SSL_CERT_DIR,
// as Go reads them); git's curl ignores SSL_CERT_FILE, so it also goes in as GIT_SSL_CAINFO.
func gitEnv(home string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	if f := os.Getenv("SSL_CERT_FILE"); f != "" {
		env = append(env, "SSL_CERT_FILE="+f, "GIT_SSL_CAINFO="+f)
	}
	if d := os.Getenv("SSL_CERT_DIR"); d != "" {
		env = append(env, "SSL_CERT_DIR="+d)
	}
	return env
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
// closure paths that cache.nixos.org does not serve, the OCI images and release assets of the
// outputs (R-OUT-013), and COMPLETE.json last. It fills the
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
	done := promote.Complete{V: 1, Job: c.JobID, Build: spec.BuildID, Project: spec.Project,
		Paths: []string{}, Images: []promote.Image{}, Assets: []promote.Asset{}, Skipped: []string{}}
	if o.Status == "failed" && len(res.Paths) > 0 {
		p.stagePartial(ctx, c, staging, work, res.Paths, &done, o)
		return
	}
	if o.Status != "ok" {
		return
	}
	if err := p.stagePaths(ctx, staging, work, res.Paths, &done); err != nil {
		*o = Outcome{Status: "infra", Error: "staging: " + err.Error(), LogLast: o.LogLast, EvalSHA: o.EvalSHA}
		return
	}
	if err := p.stageOutputs(ctx, staging, spec.Project, res, &done); err != nil {
		// an upload error is infrastructure (a new attempt retries it); anything else is an
		// image or release output that is not what bob builds: the job's own failure
		status := "failed"
		if ue := (*uploadError)(nil); errors.As(err, &ue) {
			status = "infra"
		}
		*o = Outcome{Status: status, Error: "staging images and release assets: " + err.Error(), LogLast: o.LogLast, EvalSHA: o.EvalSHA}
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

// stagePartial stages the closure of the packages that built in a failed job (decision A200):
// NARs, claims and COMPLETE.json like a succeeded job's, but no images or release assets (a
// failed job publishes nothing). The orchestrator verifies and promotes these paths like any
// other, so a retry gets them as cache hits. A staging error leaves the job failed, without a
// COMPLETE.json; it never turns the job's own failure into infrastructure.
func (p *Process) stagePartial(ctx context.Context, c Claim, staging, work string, paths []PathInfo, done *promote.Complete, o *Outcome) {
	err := p.stagePaths(ctx, staging, work, paths, done)
	var body []byte
	if err == nil {
		if body, err = json.Marshal(done); err == nil {
			_, err = p.cfg.Artifacts.Put(ctx, staging+"COMPLETE.json", body, "application/json", cache.Cond{})
		}
	}
	if err != nil {
		if p.cfg.Log != nil {
			p.cfg.Log.Warn("worker: outputs of a failed job not staged", "job", c.JobID, "err", err.Error())
		}
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
// (R-REG-005), with the upstream's signed narinfo as proof; every other path gets its zstd NAR
// and its narinfo claim.
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
		if ni, ok := upstream[pi.Path]; ok {
			// the upstream cache's signed narinfo goes with it: the orchestrator accepts a
			// skipped path as upstream only with that signature (§6.6.4, I-R2 F3)
			done.Skipped = append(done.Skipped, pi.Path)
			sem <- struct{}{}
			runs = append(runs, safe.Go(func() error {
				defer func() { <-sem }()
				_, err := p.cfg.Artifacts.Put(ctx, staging+"upstream/"+cache.HashOf(pi.Path)+".narinfo", ni, "text/x-nix-narinfo", cache.Cond{})
				if err != nil {
					return fmt.Errorf("upload upstream narinfo of %s: %w", pi.Path, err)
				}
				return nil
			}))
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

// upstream returns the narinfos of the paths cache.nixos.org serves (GET narinfo, 8 in
// parallel), by path. Without an upstream, or when it cannot be reached or answers with
// something that is not that path's narinfo, nothing is skipped: the path is staged and the
// orchestrator verifies it like any other.
func (p *Process) upstream(ctx context.Context, paths []PathInfo) map[string][]byte {
	out := map[string][]byte{}
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
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Upstream+"/"+cache.HashOf(pi.Path)+".narinfo", nil)
			if err != nil {
				return nil
			}
			res, err := hc.Do(req)
			if err != nil {
				return nil
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, cache.MaxNarInfo+1))
			if err != nil || len(body) > cache.MaxNarInfo {
				return nil
			}
			if ni, err := cache.ParseNarInfo(body); err != nil || ni.StorePath != pi.Path {
				return nil
			}
			mu.Lock()
			out[pi.Path] = body
			mu.Unlock()
			return nil
		}))
	}
	for _, r := range runs {
		<-r
	}
	return out
}
