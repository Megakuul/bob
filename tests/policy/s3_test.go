package policy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

// client is a minimal S3 client for one identity against RustFS (direct, no proxy).
type client struct {
	t     testing.TB
	base  string
	creds s3sig.Creds
}

type resp struct {
	status int
	body   string
	etag   string
}

func (c *client) do(method, path string, hdr http.Header, body []byte) resp {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	for k, v := range hdr {
		req.Header[k] = v
	}
	sum := sha256.Sum256(body)
	s3sig.Sign(req, c.creds, hex.EncodeToString(sum[:]), time.Now())
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return resp{r.StatusCode, string(b), r.Header.Get("ETag")}
}

func (r resp) ok() bool { return r.status/100 == 2 }

// presign returns a query-signed URL path (SigV4, 5 minutes) for method and path.
func (c *client) presign(method, path string) string {
	u, _ := url.Parse(c.base)
	now := time.Now().UTC()
	day, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	region := c.creds.Region
	if region == "" {
		region = "us-east-1"
	}
	scope := day + "/" + region + "/s3/aws4_request"
	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", c.creds.AccessKey+"/"+scope)
	q.Set("X-Amz-Date", stamp)
	q.Set("X-Amz-Expires", "300")
	q.Set("X-Amz-SignedHeaders", "host")
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cq []string
	for _, k := range keys {
		cq = append(cq, awsEncode(k)+"="+awsEncode(q.Get(k)))
	}
	canon := strings.Join([]string{method, awsPath(path), strings.Join(cq, "&"), "host:" + u.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	h := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	k := hm([]byte("AWS4"+c.creds.SecretKey), day)
	k = hm(k, region)
	k = hm(k, "s3")
	k = hm(k, "aws4_request")
	return path + "?" + strings.Join(cq, "&") + "&X-Amz-Signature=" + hex.EncodeToString(hm(k, toSign))
}

func hm(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

var unreserved = regexp.MustCompile(`[A-Za-z0-9_.~-]`)

func awsEncode(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		if unreserved.Match([]byte{r}) {
			b.WriteByte(r)
		} else {
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

func awsPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = awsEncode(s)
	}
	return strings.Join(parts, "/")
}

func md5b64(b []byte) string {
	s := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

// probe runs one operation of the matrix as c on tg and returns the final response (the first
// refused or failed step, else the last step). Keys are unique per call, so cells never
// interfere; the seed object tg.prefix+"obj" must exist (see seed).
type probe struct {
	c      *client
	n      int
	policy map[string]string // bucket -> bucket policy document to put back
}

func (p *probe) unique(tg target) string {
	p.n++
	return fmt.Sprintf("/%s/%sp%d", tg.bucket, tg.prefix, p.n)
}

const (
	xmlTagging   = `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`
	xmlVersionng = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Suspended</Status></VersioningConfiguration>`
	xmlLifecycle = `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ID>zz</ID><Filter><Prefix>zz/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>3650</Days></Expiration></Rule></LifecycleConfiguration>`
)

func (p *probe) run(name string, tg target) resp {
	c := p.c
	key := "/" + tg.bucket + "/" + tg.prefix + "obj"
	bucket := "/" + tg.bucket
	switch name {
	case "HeadBucket":
		return c.do("HEAD", bucket, nil, nil)
	case "ListObjectsV2":
		return c.do("GET", bucket+"?list-type=2&prefix="+url.QueryEscape(tg.prefix), nil, nil)
	case "ListObjectVersions":
		return c.do("GET", bucket+"?versions&prefix="+url.QueryEscape(tg.prefix), nil, nil)
	case "ListMultipartUploads":
		return c.do("GET", bucket+"?uploads", nil, nil)
	case "GetObject":
		return c.do("GET", key, nil, nil)
	case "HeadObject":
		return c.do("HEAD", key, nil, nil)
	case "GetObject (ranged)":
		return c.do("GET", key, http.Header{"Range": {"bytes=0-1"}}, nil)
	case "GetObjectAcl":
		return c.do("GET", key+"?acl", nil, nil)
	case "GetObjectTagging":
		return c.do("GET", key+"?tagging", nil, nil)
	case "GET (presigned)":
		return anon(c, "GET", c.presign("GET", key), nil)
	case "PutObject (new)":
		return c.do("PUT", p.unique(tg), nil, []byte("new"))
	case "PutObject (overwrite)":
		return c.do("PUT", key, nil, []byte("overwritten"))
	case "PutObject (If-None-Match)":
		return c.do("PUT", p.unique(tg), http.Header{"If-None-Match": {"*"}}, []byte("created"))
	case "PUT (presigned)":
		return anon(c, "PUT", c.presign("PUT", p.unique(tg)), []byte("presigned"))
	case "multipart upload":
		return multipart(c, p.unique(tg), "", []byte("part"))
	case "PutObjectTagging":
		return c.do("PUT", key+"?tagging", nil, []byte(xmlTagging))
	case "PutObjectAcl":
		return c.do("PUT", key+"?acl", http.Header{"X-Amz-Acl": {"private"}}, nil)
	case "CopyObject into target":
		return c.do("PUT", p.unique(tg), http.Header{"X-Amz-Copy-Source": {"/" + fleet + "/" + readableSrcKey}}, nil)
	case "CopyObject out of target":
		return c.do("PUT", "/"+fleet+"/job/copy-dst", http.Header{"X-Amz-Copy-Source": {key}}, nil)
	case "UploadPartCopy out of target":
		return multipart(c, "/"+fleet+"/job/mp-dst", key, nil)
	case "DeleteObject":
		return c.do("DELETE", key, nil, nil)
	case "DeleteObjects":
		body := []byte(`<Delete><Object><Key>` + tg.prefix + `obj</Key></Object></Delete>`)
		return c.do("POST", bucket+"?delete", http.Header{"Content-Md5": {md5b64(body)}}, body)
	case "GetBucketPolicy":
		return c.do("GET", bucket+"?policy", nil, nil)
	case "PutBucketPolicy":
		body := []byte(p.policy[tg.bucket])
		return c.do("PUT", bucket+"?policy", nil, body)
	case "PutBucketVersioning":
		return c.do("PUT", bucket+"?versioning", nil, []byte(xmlVersionng))
	case "PutBucketLifecycle":
		body := []byte(xmlLifecycle)
		return c.do("PUT", bucket+"?lifecycle", http.Header{"Content-Md5": {md5b64(body)}}, body)
	case "PutBucketAcl":
		return c.do("PUT", bucket+"?acl", http.Header{"X-Amz-Acl": {"private"}}, nil)
	}
	c.t.Fatalf("unknown operation %q", name)
	return resp{}
}

// anon sends a presigned request without an Authorization header.
func anon(c *client, method, pathQuery string, body []byte) resp {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+pathQuery, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, pathQuery, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return resp{r.StatusCode, string(b), r.Header.Get("ETag")}
}

var uploadID = regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`)

// multipart creates an upload on dst, uploads part 1 (from body, or copied from copySrc) and
// completes it (UploadPartCopy: the upload is aborted instead). It returns the first failing
// step, else the step that carries the operation under test.
func multipart(c *client, dst, copySrc string, body []byte) resp {
	c.t.Helper()
	r := c.do("POST", dst+"?uploads", nil, nil)
	if !r.ok() {
		return r
	}
	m := uploadID.FindStringSubmatch(r.body)
	if m == nil {
		c.t.Fatalf("CreateMultipartUpload on %s: no UploadId in %q", dst, r.body)
	}
	q := "?partNumber=1&uploadId=" + url.QueryEscape(m[1])
	if copySrc != "" {
		part := c.do("PUT", dst+q, http.Header{"X-Amz-Copy-Source": {copySrc}}, nil)
		c.do("DELETE", dst+"?uploadId="+url.QueryEscape(m[1]), nil, nil)
		return part
	}
	part := c.do("PUT", dst+q, nil, body)
	if !part.ok() {
		return part
	}
	done := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>` + part.etag + `</ETag></Part></CompleteMultipartUpload>`
	return c.do("POST", dst+"?uploadId="+url.QueryEscape(m[1]), nil, []byte(done))
}
