//go:build live

package forge

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// zstdRaw wraps data (<= 128 KiB per block) in a valid zstd frame made of raw blocks, so the
// tests need no compressor.
func zstdRaw(data []byte) []byte {
	out := []byte{0x28, 0xb5, 0x2f, 0xfd, 0xe0} // magic, FHD: single segment, 8-byte content size
	out = binary.LittleEndian.AppendUint64(out, uint64(len(data)))
	for first := true; first || len(data) > 0; first = false {
		n := min(len(data), 128<<10)
		last := uint32(0)
		if n == len(data) {
			last = 1
		}
		h := last | uint32(n)<<3 // block type 0 = raw
		out = append(out, byte(h), byte(h>>8), byte(h>>16))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return out
}

func tarOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, body)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func upload(e *regenv.Env, token, project, hexsum string, body io.Reader, size int64, expect bool) (int, string) {
	req, _ := http.NewRequest(http.MethodPut, e.API+"/upload/source/sha256/"+hexsum, body)
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Bob-Project", project)
	req.Header.Set("Content-Type", "application/zstd")
	if expect {
		req.Header.Set("Expect", "100-continue")
	}
	r, err := (&http.Client{Transport: &http.Transport{ExpectContinueTimeout: time.Second}, Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		e.T.Fatalf("PUT upload: %v", err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, strings.TrimSpace(string(b))
}

func TestPIPE009SourceSnapshots(t *testing.T) {
	parity.Covers(t, "R-PIPE-009")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	e.Project("other", "other")
	other := e.Principal("other-dev", "other", "API")
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	key := func(h string) string { return "src/acme/" + h + ".tar.zst" }
	body := zstdRaw(tarOf(t, map[string]string{"a.txt": "a"}))

	// a wrong hash: 400 and no object
	wrong := sum([]byte("something else"))
	if code, text := upload(e, e.Admin, "acme", wrong, bytes.NewReader(body), int64(len(body)), false); code != http.StatusBadRequest || !strings.Contains(text, "upload does not match its sha256") {
		t.Errorf("wrong hash: HTTP %d %q, want 400 \"upload does not match its sha256\"", code, text)
	}
	if code, _ := e.S3Do(e.S3Creds("root"), http.MethodGet, artifacts, key(wrong), nil); code != http.StatusNotFound {
		t.Errorf("object after a rejected upload: HTTP %d, want 404 (no object)", code)
	}
	// no CreateBuild on the project: 404
	if code, _ := upload(e, other, "acme", sum(body), bytes.NewReader(body), int64(len(body)), false); code != http.StatusNotFound {
		t.Errorf("upload without CreateBuild on the project: HTTP %d, want 404", code)
	}
	// the same upload twice: the second answers 200 in < 100 ms and sends <= 1 MiB
	big := zstdRaw(tarOf(t, map[string]string{"big.bin": strings.Repeat("0123456789abcdef", 1<<19)})) // 8 MiB
	h := sum(big)
	if code, text := upload(e, e.Admin, "acme", h, bytes.NewReader(big), int64(len(big)), true); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("first upload: HTTP %d %s", code, text)
	}
	cr := &countingReader{r: bytes.NewReader(big)}
	start := time.Now()
	code, _ := upload(e, e.Admin, "acme", h, cr, int64(len(big)), true)
	if elapsed := time.Since(start); code != http.StatusOK || elapsed > 100*time.Millisecond || cr.n > 1<<20 {
		t.Errorf("second upload: HTTP %d in %v having sent %d bytes, want 200 in < 100 ms with <= 1 MiB sent", code, elapsed, cr.n)
	}
	// a tar with ../x: the job fails with the unsafe path message
	evil := zstdRaw(tarOf(t, map[string]string{"../x": "x"}))
	if code, text := upload(e, e.Admin, "acme", sum(evil), bytes.NewReader(evil), int64(len(evil)), false); code/100 != 2 {
		t.Fatalf("upload of the unsafe tar: HTTP %d %s", code, text)
	}
	var created struct{ Build regenv.Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/CreateBuild", map[string]any{
		"projectId": "acme", "source": map[string]any{"uploadSha256": sum(evil)}, "targets": []string{"//cmd/api:api"}}, &created)
	b := e.WaitBuild(created.Build.ID, 10*time.Minute, regenv.Terminal)
	msg := "source snapshot contains an unsafe path: ../x"
	ok := b.State == "BUILD_STATE_FAILED"
	found := false
	for _, er := range b.Errors {
		found = found || strings.Contains(er.Message, msg)
	}
	if !ok || !found {
		t.Errorf("build of the unsafe snapshot: %s %+v, want FAILED with %q", b.State, b.Errors, msg)
	}
	e.AssertNoList()
}

const artifacts = "bob-artifacts"
