package cache

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// narURL returns the absolute URL of the NAR of path, from its narinfo.
func narURL(t *testing.T, e *regenv.Env, path, token string) (string, int64) {
	t.Helper()
	code, _, body := e.Get(e.API+"/nix/"+regenv.StoreHash(path)+".narinfo", token, nil)
	if code != http.StatusOK {
		t.Fatalf("narinfo of %s: HTTP %d", path, code)
	}
	var u string
	var size int64
	for _, l := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(l, "URL: "); ok {
			u = v
		}
		if v, ok := strings.CutPrefix(l, "FileSize: "); ok {
			size, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if u == "" || size == 0 {
		t.Fatalf("narinfo without URL/FileSize: %s", body)
	}
	return e.API + "/nix/" + u, size
}

func TestRREG151ProxyNeverRedirectsToS3(t *testing.T) {
	parity.Covers(t, "R-REG-151")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	_, root := publishP1(t, e)
	tok := e.Principal("p1-reader", "acme", "DOWNLOAD")
	nar, size := narURL(t, e, root, tok)
	s3, _ := e.Info["s3_url"].(string)
	s3u, _ := url.Parse(s3)

	// (3) no Location header towards S3, no presigned URL anywhere, with and without Range.
	targets := []string{e.API + "/nix/" + regenv.StoreHash(root) + ".narinfo", e.API + "/nix/" + regenv.StoreHash(root) + ".ls", nar}
	for _, u := range targets {
		for _, rng := range []string{"", "bytes=0-99", "bytes=5-", "bytes=0-1,5-9"} {
			hdr := map[string]string{}
			if rng != "" {
				hdr["Range"] = rng
			}
			code, h, body := e.Get(u, tok, hdr)
			if h.Get("Location") != "" {
				t.Errorf("(3) %s Range %q: Location header %q", u, rng, h.Get("Location"))
			}
			all := h.Get("Location") + string(body)
			for k, v := range h {
				all += k + ": " + strings.Join(v, ",") + "\n"
			}
			if strings.Contains(all, "X-Amz-Signature") || (s3u != nil && strings.Contains(all, s3u.Host)) {
				t.Errorf("(3) %s Range %q (HTTP %d): response mentions the S3 endpoint or a presigned URL", u, rng, code)
			}
		}
	}

	// Range forwarding (SPEC 6.6.3): one range -> 206 with exactly that slice, multi-range
	// -> 200 with the full body, a range past the end -> 416.
	code, h, body := e.Get(nar, tok, map[string]string{"Range": "bytes=0-99"})
	if code != http.StatusPartialContent || len(body) != 100 || !strings.HasPrefix(h.Get("Content-Range"), "bytes 0-99/") {
		t.Errorf("single range: HTTP %d, %d bytes, Content-Range %q; want 206, 100 bytes, bytes 0-99/…", code, len(body), h.Get("Content-Range"))
	}
	if code, _, body := e.Get(nar, tok, map[string]string{"Range": "bytes=0-1,5-9"}); code != http.StatusOK || int64(len(body)) != size {
		t.Errorf("multi range: HTTP %d, %d bytes; want 200 with the full %d bytes", code, len(body), size)
	}
	if code, _, _ := e.Get(nar, tok, map[string]string{"Range": "bytes=" + strconv.FormatInt(size+10, 10) + "-"}); code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("range past the end: HTTP %d, want 416", code)
	}
	e.AssertNoList()
}
