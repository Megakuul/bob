package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Download rules of SPEC R-NIX-014.
const (
	attempts       = 3
	connectTimeout = 10 * time.Second
	idleTimeout    = 60 * time.Second
)

// backoff is the wait before attempt i+1 (1 s, then 4 s).
func backoff(i int) time.Duration {
	if i == 0 {
		return time.Second
	}
	return 4 * time.Second
}

// client downloads over HTTP(S) under an egress policy.
type client struct {
	http *http.Client
}

func newClient(p Policy) *client {
	d := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	if p.DenyPrivate {
		d.Control = denyPrivate
	}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: idleTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}
	if !p.DenyPrivate {
		// A proxy would resolve names itself and bypass the address check, so a restricted
		// policy dials directly.
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &client{http: &http.Client{Transport: tr, CheckRedirect: checkRedirect}}
}

// maxRedirects caps one download's redirect chain.
const maxRedirects = 10

// pinnedKey marks a request context whose download has a pinned hash (plain http allowed).
type pinnedKey struct{}

// errRedirect marks a refused redirect (never retried).
var errRedirect = errors.New("redirect refused")

// checkRedirect applies the scheme rule of R-NIX-014 to every redirect target, not only to the
// requested URLs: without a pinned hash a redirect from https to plain http (or any other
// scheme) would let unauthenticated content be hashed and pinned.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("more than %d redirects: %w", maxRedirects, errRedirect)
	}
	switch req.URL.Scheme {
	case "https":
		return nil
	case "http":
		if pinned, _ := req.Context().Value(pinnedKey{}).(bool); pinned {
			return nil
		}
		return fmt.Errorf("redirect to %s uses plain http and the request has no pinned hash: %w", req.URL.Redacted(), errRedirect)
	}
	return fmt.Errorf("redirect to %s is not an https URL: %w", req.URL.Redacted(), errRedirect)
}

// plainHTTPAllowed reports whether a requested plain http URL of host may be downloaded: only
// with a pinned hash, except loopback hosts in test builds (testLoopbackHTTP). Redirects keep
// the strict rule (checkRedirect).
func plainHTTPAllowed(host string, pinned bool) bool {
	if pinned {
		return true
	}
	if !testLoopbackHTTP {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// errDenied marks a connection the egress policy refused (never retried).
var errDenied = errors.New("not allowed by the egress policy")

// denyPrivate refuses connections to loopback, private, link-local and unspecified addresses.
func denyPrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("address %s: %w", host, errDenied)
	}
	return nil
}

// attemptError is the result of one failed attempt.
type attemptError struct {
	notFound bool  // HTTP 404/410: the next mirror, no retry
	retry    bool  // transient: retry this URL
	status   int   // HTTP status, 0 if none
	diag     error // a final diagnostic (hash mismatch, size cap)
	err      error
}

func (e *attemptError) Error() string { return e.err.Error() }

// download fetches the first mirror that delivers into file and returns the URL and the digest
// (algorithm algo) of the bytes. A pinned download hash is verified here, before unpacking.
func (c *client) download(ctx context.Context, p *prepared, file, algo string) (string, []byte, error) {
	pinned := p.hash != nil || p.narHash != nil
	for _, u := range p.URLs {
		pu, err := url.Parse(u)
		if err != nil || pu.Host == "" {
			return "", nil, diag.New(3002, "label", p.Label, "reason", fmt.Sprintf("%q is not a download URL", u))
		}
		switch pu.Scheme {
		case "https":
		case "http":
			if !plainHTTPAllowed(pu.Hostname(), pinned) {
				return "", nil, diag.New(3002, "label", p.Label, "reason", u+" uses plain http and the request has no pinned hash")
			}
		default:
			return "", nil, diag.New(3002, "label", p.Label, "reason", u+" is not an https URL")
		}
	}
	ctx = context.WithValue(ctx, pinnedKey{}, pinned)
	var last *attemptError
	var final error
	for _, u := range p.URLs {
		for i := range attempts {
			if i > 0 {
				t := time.NewTimer(backoff(i - 1))
				select {
				case <-ctx.Done():
					t.Stop()
					return "", nil, fmt.Errorf("fetch %s: %w", p.Label, ctx.Err())
				case <-t.C:
				}
			}
			digest, aerr := c.get(ctx, u, file, algo, p.MaxBytes)
			if aerr == nil {
				if p.hash != nil && !bytes.Equal(digest, p.hash.digest) {
					got := sri{algo: algo, digest: digest}
					aerr = &attemptError{diag: mismatch(p.Label, u, p.hash.String(), got.String()), err: errors.New("hash mismatch")}
				} else {
					return u, digest, nil
				}
			}
			if ctx.Err() != nil {
				return "", nil, fmt.Errorf("fetch %s: %w", p.Label, ctx.Err())
			}
			last = aerr
			if aerr.diag != nil && final == nil {
				final = aerr.diag
			}
			if !aerr.retry {
				break
			}
		}
	}
	switch {
	case final != nil:
		return "", nil, final
	case last != nil && last.notFound:
		return "", nil, diag.New(3003, "label", p.Label, "status", strconv.Itoa(last.status))
	case last != nil:
		e := diag.New(3002, "label", p.Label, "reason", last.err.Error())
		if last.retry {
			e.Args["attempts"] = strconv.Itoa(attempts)
		}
		return "", nil, e
	}
	return "", nil, fmt.Errorf("fetch %s: nothing to download", p.Label)
}

// get makes one attempt: it streams the body of u into file, hashing it.
func (c *client) get(ctx context.Context, u, file, algo string, maxBytes int64) ([]byte, *attemptError) {
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &attemptError{err: err}
	}
	req.Header.Set("User-Agent", "bob")
	resp, err := c.http.Do(req)
	if err != nil {
		retry := !errors.Is(err, errDenied) && !errors.Is(err, errRedirect)
		return nil, &attemptError{retry: retry, err: fmt.Errorf("%s: %w", u, err)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, &attemptError{notFound: true, status: resp.StatusCode, err: fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)}
	default:
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout
		return nil, &attemptError{retry: retry, status: resp.StatusCode, err: fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)}
	}
	if resp.ContentLength > maxBytes {
		return nil, sizeError(u, maxBytes)
	}
	out, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, &attemptError{err: err}
	}
	defer out.Close()
	h := newHash(algo)
	body := &idleReader{r: resp.Body, cancel: cancel}
	body.arm()
	defer body.stop()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(body, maxBytes+1))
	if err != nil {
		if body.timedOut() {
			err = fmt.Errorf("no data for %s", idleTimeout)
		}
		return nil, &attemptError{retry: true, err: fmt.Errorf("%s: %w", u, err)}
	}
	if n > maxBytes {
		return nil, sizeError(u, maxBytes)
	}
	if err := out.Close(); err != nil {
		return nil, &attemptError{err: err}
	}
	return h.Sum(nil), nil
}

func sizeError(u string, maxBytes int64) *attemptError {
	return &attemptError{err: fmt.Errorf("%s is larger than the limit of %d bytes", u, maxBytes)}
}

// idleReader cancels the request when no data arrives for idleTimeout.
type idleReader struct {
	r      io.Reader
	cancel context.CancelFunc
	mu     sync.Mutex
	timer  *time.Timer
	fired  bool
}

func (r *idleReader) arm() {
	r.timer = time.AfterFunc(idleTimeout, func() {
		r.mu.Lock()
		r.fired = true
		r.mu.Unlock()
		r.cancel()
	})
}

func (r *idleReader) stop() { r.timer.Stop() }

func (r *idleReader) timedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fired
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(idleTimeout)
	}
	return n, err
}
