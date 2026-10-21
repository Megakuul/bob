// Package s3sig signs S3 requests with AWS Signature Version 4 for the harness's
// own clients (bucket setup, probes, planted requests) and for the counting
// proxy when a fault rewrites a signed header. It keeps the harness free of an
// S3 SDK dependency.
package s3sig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// UnsignedPayload is the x-amz-content-sha256 value for an unhashed body.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// Creds is one S3 identity.
type Creds struct {
	AccessKey, SecretKey, Region string
}

// Sign adds the SigV4 Authorization header to req. payloadHash is the hex
// SHA-256 of the body, or UnsignedPayload. Host, Content-Type, Content-MD5 and
// every X-Amz-* and If-* header are signed; hop-by-hop and transport headers are
// not, so a proxy may change them.
func Sign(req *http.Request, c Creds, payloadHash string, now time.Time) {
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Del("Authorization")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	names := []string{"host"}
	vals := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk != "content-type" && lk != "content-md5" && !strings.HasPrefix(lk, "x-amz-") && !strings.HasPrefix(lk, "if-") {
			continue
		}
		names = append(names, lk)
		vals[lk] = strings.TrimSpace(strings.Join(v, ","))
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")

	canon := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	k := mac([]byte("AWS4"+c.SecretKey), day)
	k = mac(k, region)
	k = mac(k, "s3")
	k = mac(k, "aws4_request")
	sig := hex.EncodeToString(mac(k, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signed, sig))
}

// AccessKey returns the access key id of a SigV4-signed request (header or
// presigned query form), or "" when the request is anonymous.
func AccessKey(req *http.Request) string {
	cred := req.URL.Query().Get("X-Amz-Credential")
	if a := req.Header.Get("Authorization"); a != "" {
		_, rest, ok := strings.Cut(a, "Credential=")
		if !ok {
			return ""
		}
		cred, _, _ = strings.Cut(rest, ",")
	}
	ak, _, _ := strings.Cut(cred, "/")
	return strings.TrimSpace(ak)
}

func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func canonicalPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	// S3 does not normalise the path; only re-encode what EscapedPath left alone
	// but SigV4 requires encoded.
	return strings.NewReplacer("!", "%21", "'", "%27", "(", "%28", ")", "%29", "*", "%2A", "$", "%24", "+", "%2B", ",", "%2C", ";", "%3B", "=", "%3D", "@", "%40", ":", "%3A").Replace(p)
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if k == "X-Amz-Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, encode(k)+"="+encode(v))
		}
	}
	return strings.Join(parts, "&")
}

// encode is RFC 3986 percent-encoding as SigV4 wants it.
func encode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
