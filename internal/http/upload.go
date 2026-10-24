package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"
)

// UploadPattern is the mux pattern of the source upload (SPEC §6.2, §6.9.7).
const UploadPattern = "PUT /upload/source/sha256/{hex}"

// Defaults of SourceUpload.
const (
	DefaultMaxUpload      = 512 << 20 // compressed snapshot cap (§6.9.7 [proposal], = the CLI's cap)
	DefaultParallelUpload = 4         // uploads buffered on disk at the same time
	uploadIdle            = 60 * time.Second
)

// Answers of SourceUpload.Authorize.
var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrNoAccess        = errors.New("no access")
)

// Sources stores source snapshots under src/<project>/<sha>.tar.zst in the artifacts bucket.
type Sources interface {
	Has(ctx context.Context, project, sha string) (bool, error)
	// Create writes the snapshot create-only; an existing object is not an error (the key is
	// content-addressed and every write is verified first).
	Create(ctx context.Context, project, sha string, r io.ReadSeeker, size int64) error
}

// SourceUpload is PUT /upload/source/sha256/<hex> (SPEC §6.9.7, R-PIPE-009): the zstd tar of a
// `--remote` build's sources, content-addressed and scoped to the project of `Bob-Project`.
// The body goes to a temporary file while it is hashed; only bytes that match the hash in the
// path are written to the bucket, so a wrong upload leaves no object behind. An existing
// snapshot is answered 200 before the body is read (the client sends Expect: 100-continue).
type SourceUpload struct {
	// Authorize authenticates r (Bearer API token) and checks BuildService/CreateBuild on
	// project: nil = allowed, ErrUnauthenticated → 401, ErrNoAccess → 404, else 503.
	Authorize func(r *http.Request, project string) error
	Sources   Sources
	Max       int64  // 0 = DefaultMaxUpload
	Parallel  int    // 0 = DefaultParallelUpload
	TempDir   string // "" = os.TempDir()
	Log       *slog.Logger

	slots chan struct{}
}

var (
	shaRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	projectRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
)

// NewSourceUpload fills the defaults of u.
func NewSourceUpload(u SourceUpload) *SourceUpload {
	if u.Max <= 0 {
		u.Max = DefaultMaxUpload
	}
	if u.Parallel <= 0 {
		u.Parallel = DefaultParallelUpload
	}
	if u.Log == nil {
		u.Log = slog.Default()
	}
	u.slots = make(chan struct{}, u.Parallel)
	return &u
}

func (u *SourceUpload) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("hex")
	project := r.Header.Get("Bob-Project")
	switch {
	case !shaRE.MatchString(sha):
		http.Error(w, "invalid sha256: want 64 lower-case hex characters", http.StatusBadRequest)
		return
	case !projectRE.MatchString(project):
		http.Error(w, "the Bob-Project header must name a project id", http.StatusBadRequest)
		return
	}
	if err := u.Authorize(r, project); err != nil {
		switch {
		case errors.Is(err, ErrUnauthenticated):
			w.Header().Set("WWW-Authenticate", `Bearer realm="bob"`)
			http.Error(w, "unauthorized: an API token is required (run 'bob login')", http.StatusUnauthorized)
		case errors.Is(err, ErrNoAccess):
			http.Error(w, "project not found", http.StatusNotFound)
		default:
			u.unavailable(w, "authorize", err)
		}
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/zstd" {
		http.Error(w, "Content-Type must be application/zstd", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > u.Max {
		http.Error(w, fmt.Sprintf("source snapshot too large: at most %d bytes", u.Max), http.StatusRequestEntityTooLarge)
		return
	}
	ctx := r.Context()
	have, err := u.Sources.Has(ctx, project, sha)
	if err != nil {
		u.unavailable(w, "head", err)
		return
	}
	if have {
		w.WriteHeader(http.StatusOK)
		return
	}
	select {
	case u.slots <- struct{}{}:
		defer func() { <-u.slots }()
	case <-ctx.Done():
		return
	}
	code, msg := u.receive(w, r, project, sha)
	if code >= 500 {
		u.Log.Warn("source upload failed", "project", project, "sha256", sha, "err", msg)
		msg = "storage unavailable"
	}
	if code == http.StatusCreated {
		u.Log.Info("source uploaded", "project", project, "sha256", sha)
		w.WriteHeader(code)
		return
	}
	http.Error(w, msg, code)
}

// receive spools the body to a temporary file while hashing it and, when it matches sha,
// writes it to the bucket.
func (u *SourceUpload) receive(w http.ResponseWriter, r *http.Request, project, sha string) (int, string) {
	f, err := os.CreateTemp(u.TempDir, "bob-upload-*.tar.zst")
	if err != nil {
		return http.StatusServiceUnavailable, err.Error()
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	h := sha256.New()
	body := &idleReader{r: r.Body, rc: http.NewResponseController(w)}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, u.Max+1))
	switch {
	case n > u.Max:
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("source snapshot too large: at most %d bytes", u.Max)
	case err != nil && body.failed:
		return http.StatusBadRequest, "upload interrupted: " + err.Error()
	case err != nil:
		return http.StatusServiceUnavailable, err.Error()
	case r.ContentLength >= 0 && n != r.ContentLength:
		return http.StatusBadRequest, "upload shorter than its Content-Length"
	case hex.EncodeToString(h.Sum(nil)) != sha:
		return http.StatusBadRequest, "upload does not match its sha256"
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return http.StatusServiceUnavailable, err.Error()
	}
	if err := u.Sources.Create(r.Context(), project, sha, f, n); err != nil {
		return http.StatusServiceUnavailable, err.Error()
	}
	return http.StatusCreated, ""
}

func (u *SourceUpload) unavailable(w http.ResponseWriter, step string, err error) {
	u.Log.Warn("source upload: "+step, "err", err.Error())
	w.Header().Set("Retry-After", strconv.Itoa(1))
	http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
}

// idleReader reads the request body with a read deadline of uploadIdle per read, so a stalled
// client cannot hold an upload slot (the server has no overall read timeout: uploads are long).
// failed records that the client side failed (not the temporary file).
type idleReader struct {
	r      io.Reader
	rc     *http.ResponseController
	failed bool
}

func (i *idleReader) Read(p []byte) (int, error) {
	_ = i.rc.SetReadDeadline(time.Now().Add(uploadIdle))
	n, err := i.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		i.failed = true
	}
	return n, err
}
