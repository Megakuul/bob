package promote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/internal/cache"
)

// verifyError is a staged object that fails verification: the job promotion becomes
// "failed: artifact verification" (never retried). Every other error is retried.
type verifyError struct{ msg string }

func (e *verifyError) Error() string { return e.msg }

func verifyFailed(format string, args ...any) error {
	return &verifyError{msg: fmt.Sprintf(format, args...)}
}

// VerifyFailed is a verification failure for the Images hook (the registry, M16.3): the job's
// promotion ends Failed instead of being retried.
func VerifyFailed(format string, args ...any) error { return verifyFailed(format, args...) }

func isVerify(err error) bool {
	var v *verifyError
	return errors.As(err, &v)
}

// storageReader marks errors of the underlying download so that a broken connection is
// retried instead of being taken for a bad NAR.
type storageReader struct {
	r   io.Reader
	err error
}

func (s *storageReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

type counter struct{ n uint64 }

func (c *counter) Write(p []byte) (int, error) { c.n += uint64(len(p)); return len(p), nil }

// isNix32 reports whether c belongs to Nix's base32 alphabet (no e, o, t, u).
func isNix32(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' && c != 'e' && c != 'o' && c != 't' && c != 'u'
}

const (
	storePrefix = "/nix/store/"
	scanCarry   = len(storePrefix) + 31
	maxExtra    = 256
)

// refScanner finds store hashes in the NAR bytes (SPEC §6.6.4 step 3b): hashes of known
// paths (the job's closure), and hashes after "/nix/store/" that are not known (checked
// against the served narinfos afterwards).
type refScanner struct {
	known map[string]bool
	found map[string]bool
	extra map[string]bool
	// overflow: more than maxExtra unknown hashes; the rest were not recorded, so the scan is
	// incomplete and verification must fail (I-R2 F4)
	overflow bool
	tail     []byte
}

func newRefScanner(known map[string]bool) *refScanner {
	return &refScanner{known: known, found: map[string]bool{}, extra: map[string]bool{}}
}

func (s *refScanner) scan(b []byte) {
	for i := 0; i+32 <= len(b); {
		j := i + 31
		for j >= i && isNix32(b[j]) {
			j--
		}
		if j >= i {
			i = j + 1
			continue
		}
		w := b[i : i+32]
		switch {
		case s.known[string(w)]:
			s.found[string(w)] = true
		case i >= len(storePrefix) && string(b[i-len(storePrefix):i]) == storePrefix && !s.extra[string(w)]:
			if len(s.extra) < maxExtra {
				s.extra[string(w)] = true
			} else {
				s.overflow = true
			}
		}
		i++
	}
}

func (s *refScanner) Write(p []byte) (int, error) {
	if len(s.tail) > 0 {
		// windows that start in the previous chunk
		edge := append(bytes.Clone(s.tail), p[:min(len(p), scanCarry)]...)
		s.scan(edge)
	}
	s.scan(p)
	if len(p) >= scanCarry {
		s.tail = append(s.tail[:0], p[len(p)-scanCarry:]...)
	} else {
		s.tail = append(s.tail, p...)
		if len(s.tail) > scanCarry {
			s.tail = s.tail[len(s.tail)-scanCarry:]
		}
	}
	return len(p), nil
}

// verified is what one staged NAR proved.
type verified struct {
	NarHash, FileHash string
	NarSize, FileSize uint64
	ETag              string
	Listing           *os.File // the .ls document; the caller removes it
	ListingSize       int64
	Found             map[string]bool // known hashes found in the NAR
	Extra             map[string]bool // other hashes after /nix/store/
	Overflow          bool            // more than maxExtra of those: Extra is incomplete
	// Top holds the root directory's entries by name (≤ maxTop; the kind check, A149).
	Top map[string]cache.NarEntry
}

func (v *verified) close() {
	if v != nil && v.Listing != nil {
		_ = v.Listing.Close()
		_ = os.Remove(v.Listing.Name())
	}
}

func sum(h hash.Hash) string { return "sha256:" + cache.Nix32(h.Sum(nil)) }

// verifyNAR streams the staged NAR at key once: zstd decode → structure check → NarHash,
// NarSize, FileHash, FileSize recomputed → reference scan → .ls listing (to a temp file).
// Memory stays bounded whatever the NAR size.
func verifyNAR(ctx context.Context, obj *cache.Objects, key string, known map[string]bool) (_ *verified, err error) {
	o, err := obj.Get(ctx, key, "", "")
	if errors.Is(err, cache.ErrNotFound) {
		return nil, verifyFailed("staged NAR %s is missing", key)
	}
	if err != nil {
		return nil, err
	}
	defer o.Body.Close()
	ls, err := os.CreateTemp("", "bob-ls-*")
	if err != nil {
		return nil, fmt.Errorf("promote: listing: %w", err)
	}
	v := &verified{ETag: o.ETag, Listing: ls, Top: map[string]cache.NarEntry{}}
	defer func() {
		if err != nil {
			v.close()
		}
	}()
	body := &storageReader{r: o.Body}
	fileHash, fileCount := sha256.New(), &counter{}
	compressed := io.TeeReader(body, io.MultiWriter(fileHash, fileCount))
	dec, err := zstd.NewReader(compressed, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		return nil, fmt.Errorf("promote: zstd: %w", err)
	}
	defer dec.Close()
	narHash, narCount := sha256.New(), &counter{}
	scanner := newRefScanner(known)
	decoded := io.TeeReader(io.LimitReader(dec, cache.MaxNARSize+1), io.MultiWriter(narHash, narCount, scanner))
	listing := cache.NewListing(ls)
	var local error // the listing's own I/O (temp file): retried, not the NAR's fault
	werr := cache.WalkNAR(decoded, func(e cache.NarEntry) error {
		if n, ok := strings.CutPrefix(e.Path, "/"); ok && !strings.Contains(n, "/") && len(v.Top) < maxTop {
			v.Top[n] = e
		}
		if err := listing.Add(e); err != nil {
			if !errors.Is(err, cache.ErrBadNAR) {
				local = err
			}
			return err
		}
		return nil
	})
	switch {
	case body.err != nil:
		return nil, fmt.Errorf("promote: read %s: %w", key, body.err)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case local != nil:
		return nil, fmt.Errorf("promote: listing: %w", local)
	case werr != nil:
		// whatever else broke the walk is the staged bytes: zstd framing or NAR structure
		return nil, verifyFailed("staged NAR %s: %v", key, werr)
	}
	if narCount.n > cache.MaxNARSize {
		return nil, verifyFailed("staged NAR %s is larger than %d bytes", key, uint64(cache.MaxNARSize))
	}
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return nil, fmt.Errorf("promote: read %s: %w", key, err)
	}
	if o.Length > 0 && fileCount.n != uint64(o.Length) {
		return nil, verifyFailed("staged NAR %s: read %d of %d bytes", key, fileCount.n, o.Length)
	}
	if err := listing.Close(); err != nil {
		return nil, fmt.Errorf("promote: listing: %w", err)
	}
	fi, err := ls.Stat()
	if err != nil {
		return nil, fmt.Errorf("promote: listing: %w", err)
	}
	v.NarHash, v.NarSize = sum(narHash), narCount.n
	v.FileHash, v.FileSize = sum(fileHash), fileCount.n
	v.ListingSize, v.Found, v.Extra, v.Overflow = fi.Size(), scanner.found, scanner.extra, scanner.overflow
	return v, nil
}

// hashStream returns the sha256 (hex) and size of the object at key (asset verification).
func hashStream(ctx context.Context, obj *cache.Objects, key string) (string, int64, error) {
	o, err := obj.Get(ctx, key, "", "")
	if err != nil {
		return "", 0, err
	}
	defer o.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, o.Body)
	if err != nil {
		return "", 0, fmt.Errorf("promote: read %s: %w", key, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}
