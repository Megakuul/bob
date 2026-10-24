package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"bobspike/s11/internal/s3x"
)

// target is a bucket plus key prefix whose access we probe.
type target struct {
	name, bucket, prefix string
}

var targets = []target{
	{"control", "bob-control", "user/"},
	{"fleet:worker/ (orch-owned)", "bob-fleet", "worker/"},
	{"fleet:wreg/ (worker-owned)", "bob-fleet", "wreg/"},
	{"analytics", "bob-analytics", "logs/"},
	{"artifacts:nar/", "bob-artifacts", "nar/"},
	{"artifacts:staging/", "bob-artifacts", "staging/"},
}

type opFn func(ctx context.Context, c *s3.Client, cr s3x.Creds, t target, seed string) error

type op struct {
	name string
	f    opFn
	// kind decides the expected result for the worker role.
	kind string // list|read|write|delete|bucketadmin|readsrc|writedst|global
}

func ops(endpoint string) []op {
	return []op{
		{"HeadBucket", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &t.bucket})
			return err
		}, "list"},
		{"ListObjectsV2(prefix)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &t.bucket, Prefix: &t.prefix})
			return err
		}, "list"},
		{"ListObjectVersions", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: &t.bucket})
			return err
		}, "list"},
		{"ListMultipartUploads", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: &t.bucket})
			return err
		}, "list"},
		{"GetObject", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			r, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &t.bucket, Key: &seed})
			if err == nil {
				r.Body.Close()
			}
			return err
		}, "read"},
		{"HeadObject", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &t.bucket, Key: &seed})
			return err
		}, "read"},
		{"GetObject Range", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			r, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &t.bucket, Key: &seed, Range: aws.String("bytes=0-1")})
			if err == nil {
				r.Body.Close()
			}
			return err
		}, "read"},
		{"GetObjectAcl", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: &t.bucket, Key: &seed})
			return err
		}, "readmeta"},
		{"GetObjectTagging", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: &t.bucket, Key: &seed})
			return err
		}, "readmeta"},
		{"PresignedGET", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			ps := s3.NewPresignClient(c)
			req, err := ps.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &t.bucket, Key: &seed}, s3.WithPresignExpires(5*time.Minute))
			if err != nil {
				return err
			}
			return httpStatus(http.Get(req.URL))
		}, "read"},
		{"PutObject", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			return s3x.Put(ctx, c, t.bucket, t.prefix+"new-put", "x")
		}, "write"},
		{"PutObject overwrite", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			return s3x.Put(ctx, c, t.bucket, seed, "overwritten")
		}, "write"},
		{"PutObject If-None-Match:*", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			k := t.prefix + "new-inm-" + fmt.Sprint(time.Now().UnixNano())
			_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: &t.bucket, Key: &k, Body: strings.NewReader("x"), IfNoneMatch: aws.String("*")})
			return err
		}, "write"},
		{"PresignedPUT", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			ps := s3.NewPresignClient(c)
			k := t.prefix + "new-presigned"
			req, err := ps.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &t.bucket, Key: &k}, s3.WithPresignExpires(5*time.Minute))
			if err != nil {
				return err
			}
			hr, _ := http.NewRequest(http.MethodPut, req.URL, strings.NewReader("x"))
			for k, v := range req.SignedHeader {
				hr.Header[k] = v
			}
			return httpStatus(http.DefaultClient.Do(hr))
		}, "write"},
		{"Multipart upload", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			return multipart(ctx, c, t.bucket, t.prefix+"new-mpu", nil)
		}, "write"},
		{"PutObjectTagging", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{Bucket: &t.bucket, Key: &seed,
				Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("a"), Value: aws.String("b")}}}})
			return err
		}, "writemeta"},
		{"PutObjectAcl public-read", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.PutObjectAcl(ctx, &s3.PutObjectAclInput{Bucket: &t.bucket, Key: &seed, ACL: types.ObjectCannedACLPublicRead})
			return err
		}, "writemeta"},
		{"CopyObject into target (src fleet/wreg)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			k := t.prefix + "new-copy"
			_, err := c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &t.bucket, Key: &k, CopySource: aws.String("bob-fleet/wreg/seed-src")})
			return err
		}, "write"},
		{"CopyObject from target (dst fleet/wreg)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String("bob-fleet"), Key: aws.String("wreg/exfil-copy"), CopySource: aws.String(t.bucket + "/" + seed)})
			return err
		}, "read"},
		{"UploadPartCopy from target (dst fleet/wreg)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			src := t.bucket + "/" + seed
			return multipart(ctx, c, "bob-fleet", "wreg/exfil-upc", &src)
		}, "read"},
		{"DeleteObject", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			_, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &t.bucket, Key: &seed})
			if err == nil {
				return stillThere(ctx, t.bucket, seed)
			}
			return err
		}, "delete"},
		{"DeleteObjects (batch)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, seed string) error {
			out, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &t.bucket, Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: &seed}}}})
			if err != nil {
				return err
			}
			if len(out.Errors) > 0 {
				return fmt.Errorf("StatusCode: 403 per-key error %s", aws.ToString(out.Errors[0].Code))
			}
			return stillThere(ctx, t.bucket, seed)
		}, "delete"},
		{"GetBucketPolicy", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: &t.bucket})
			if s3x.Class(err) == "ERR:NoSuchBucketPolicy" {
				return nil
			}
			return err
		}, "bucketadmin"},
		{"PutBucketPolicy (grant *)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			pol := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:*"],"Resource":["arn:aws:s3:::%s","arn:aws:s3:::%s/*"]}]}`, t.bucket, t.bucket)
			_, err := c.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: &t.bucket, Policy: &pol})
			return err
		}, "bucketadmin"},
		{"PutBucketVersioning", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: &t.bucket,
				VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusSuspended}})
			return err
		}, "bucketadmin"},
		{"PutBucketLifecycle (expire all)", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: &t.bucket,
				LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{
					ID: aws.String("x"), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")},
					Expiration: &types.LifecycleExpiration{Days: aws.Int32(1)}}}}})
			return err
		}, "bucketadmin"},
		{"PutBucketAcl public-read-write", func(ctx context.Context, c *s3.Client, _ s3x.Creds, t target, _ string) error {
			_, err := c.PutBucketAcl(ctx, &s3.PutBucketAclInput{Bucket: &t.bucket, ACL: types.BucketCannedACLPublicReadWrite})
			return err
		}, "bucketadmin"},
	}
}

// globalOps run once per credential, not per target.
func globalOps(endpoint string) []op {
	return []op{
		{"ListBuckets", func(ctx context.Context, c *s3.Client, _ s3x.Creds, _ target, _ string) error {
			_, err := c.ListBuckets(ctx, &s3.ListBucketsInput{})
			return err
		}, "global"},
		{"CreateBucket bob-evil", func(ctx context.Context, c *s3.Client, _ s3x.Creds, _ target, _ string) error {
			_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("bob-evil")})
			return err
		}, "bucketadmin"},
		{"STS AssumeRole (no policy)", func(ctx context.Context, _ *s3.Client, cr s3x.Creds, _ target, _ string) error {
			return signedForm(ctx, endpoint, cr, "sts", url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"},
				"RoleArn": {"arn:aws:iam::000000000000:role/bob-worker"}, "RoleSessionName": {"x"}, "DurationSeconds": {"900"}})
		}, "admin"},
		{"IAM ListUsers (SeaweedFS IAM API)", func(ctx context.Context, _ *s3.Client, cr s3x.Creds, _ target, _ string) error {
			return signedForm(ctx, endpoint, cr, "iam", url.Values{"Action": {"ListUsers"}, "Version": {"2010-05-08"}})
		}, "admin"},
		{"IAM CreateAccessKey (SeaweedFS IAM API)", func(ctx context.Context, _ *s3.Client, cr s3x.Creds, _ target, _ string) error {
			return signedForm(ctx, endpoint, cr, "iam", url.Values{"Action": {"CreateAccessKey"}, "Version": {"2010-05-08"}, "UserName": {cr.Name}})
		}, "admin"},
		{"IAM PutUserPolicy self s3:* (SeaweedFS IAM API)", func(ctx context.Context, _ *s3.Client, cr s3x.Creds, _ target, _ string) error {
			return signedForm(ctx, endpoint, cr, "iam", url.Values{"Action": {"PutUserPolicy"}, "Version": {"2010-05-08"}, "UserName": {cr.Name},
				"PolicyName": {"evil"}, "PolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]}]}`}})
		}, "admin"},
		{"Admin list-users (RustFS/MinIO admin API)", func(ctx context.Context, _ *s3.Client, cr s3x.Creds, _ target, _ string) error {
			return signedGet(ctx, endpoint+"/rustfs/admin/v3/list-users", cr)
		}, "admin"},
		{"Admin info-canned-policy (RustFS admin API)", func(ctx context.Context, _ *s3.Client, cr s3x.Creds, _ target, _ string) error {
			return signedGet(ctx, endpoint+"/rustfs/admin/v3/info-canned-policy?name=bob-worker", cr)
		}, "admin"},
	}
}

func httpStatus(r *http.Response, err error) error {
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 512))
	if r.StatusCode/100 == 2 {
		return nil
	}
	if r.StatusCode == 403 || r.StatusCode == 401 {
		return fmt.Errorf("StatusCode: 403 %s", snippet(b))
	}
	return fmt.Errorf("http %d %s", r.StatusCode, snippet(b))
}

func snippet(b []byte) string {
	s := strings.ReplaceAll(string(b), "\n", " ")
	if i := strings.Index(s, "<Code>"); i >= 0 {
		if j := strings.Index(s[i:], "</Code>"); j > 0 {
			return s[i+6 : i+j]
		}
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func signedForm(ctx context.Context, endpoint string, cr s3x.Creds, service string, form url.Values) error {
	body := form.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doSigned(ctx, req, []byte(body), cr, service)
}

func signedGet(ctx context.Context, u string, cr s3x.Creds) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	return doSigned(ctx, req, nil, cr, "s3")
}

func doSigned(ctx context.Context, req *http.Request, body []byte, cr s3x.Creds, service string) error {
	h := sha256.Sum256(body)
	ph := hex.EncodeToString(h[:])
	req.Header.Set("X-Amz-Content-Sha256", ph)
	if cr.Session != "" {
		req.Header.Set("X-Amz-Security-Token", cr.Session)
	}
	err := v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: cr.AK, SecretAccessKey: cr.SK, SessionToken: cr.Session}, req, ph, service, "us-east-1", time.Now())
	if err != nil {
		return err
	}
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return httpStatus(http.DefaultClient.Do(req))
}

func multipart(ctx context.Context, c *s3.Client, bucket, key string, copySrc *string) error {
	mpu, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return err
	}
	var etag *string
	if copySrc != nil {
		out, err := c.UploadPartCopy(ctx, &s3.UploadPartCopyInput{Bucket: &bucket, Key: &key, UploadId: mpu.UploadId, PartNumber: aws.Int32(1), CopySource: copySrc})
		if err != nil {
			c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &bucket, Key: &key, UploadId: mpu.UploadId})
			return err
		}
		etag = out.CopyPartResult.ETag
	} else {
		out, err := c.UploadPart(ctx, &s3.UploadPartInput{Bucket: &bucket, Key: &key, UploadId: mpu.UploadId, PartNumber: aws.Int32(1), Body: strings.NewReader("part")})
		if err != nil {
			c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &bucket, Key: &key, UploadId: mpu.UploadId})
			return err
		}
		etag = out.ETag
	}
	_, err = c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &bucket, Key: &key, UploadId: mpu.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{ETag: etag, PartNumber: aws.Int32(1)}}}})
	return err
}

var adminClient *s3.Client

// resetBucket undoes bucket-level changes so one probe cannot influence the next.
func resetBucket(ctx context.Context, b string) {
	adminClient.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: &b})
	adminClient.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: &b})
	adminClient.PutBucketAcl(ctx, &s3.PutBucketAclInput{Bucket: &b, ACL: types.BucketCannedACLPrivate})
}

// effect checks with the admin credential whether an "OK" bucket-admin call really changed state.
func effect(ctx context.Context, b, name string) string {
	switch {
	case strings.HasPrefix(name, "PutBucketLifecycle"):
		out, err := adminClient.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: &b})
		if err == nil && len(out.Rules) > 0 {
			return " (stored)"
		}
		return " (not stored)"
	case strings.HasPrefix(name, "PutBucketPolicy"):
		_, err := adminClient.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: &b})
		if err == nil {
			return " (stored)"
		}
		return " (not stored)"
	case strings.HasPrefix(name, "PutBucketVersioning"):
		out, err := adminClient.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &b})
		if err == nil && out.Status != "" {
			return " (stored: " + string(out.Status) + ")"
		}
		return " (not stored)"
	case strings.HasPrefix(name, "PutBucketAcl"):
		out, err := adminClient.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: &b})
		if err == nil {
			for _, g := range out.Grants {
				if g.Grantee != nil && g.Grantee.URI != nil {
					return " (stored: " + string(g.Permission) + " to " + *g.Grantee.URI + ")"
				}
			}
		}
		return " (no public grant visible)"
	}
	return ""
}

// stillThere reports an error if a "successful" delete left the object in place.
func stillThere(ctx context.Context, bucket, key string) error {
	_, err := adminClient.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err == nil {
		return fmt.Errorf("delete returned OK but object still exists (silently ignored)")
	}
	return nil
}

// expectWorker returns the expected class for the worker role.
func expectWorker(t target, o op) string {
	allowRead := t.bucket != "bob-control"
	allowWrite := t.bucket == "bob-analytics" || t.prefix == "wreg/" || t.prefix == "staging/"
	allowDelete := t.bucket == "bob-analytics"
	switch o.kind {
	case "list":
		if o.name == "ListObjectsV2(prefix)" && allowRead && t.bucket != "bob-artifacts" {
			return "OK"
		}
		if o.name == "HeadBucket" && allowRead && t.bucket != "bob-artifacts" {
			return "OK"
		}
		return "DENIED"
	case "read":
		if allowRead {
			return "OK"
		}
		return "DENIED"
	case "write":
		if allowWrite {
			return "OK"
		}
		return "DENIED"
	case "delete":
		if allowDelete {
			return "OK"
		}
		return "DENIED"
	}
	return "DENIED" // readmeta, writemeta, bucketadmin, admin
}

func runMatrix(ctx context.Context, server, endpoint string, root, worker, orch s3x.Creds) int {
	adminClient = s3x.Client(endpoint, root)
	fails := 0
	fmt.Printf("\n### %s: worker credential\n\n| target | operation | expected | worker | orchestrator | |\n|---|---|---|---|---|---|\n", server)
	for _, t := range targets {
		for _, o := range ops(endpoint) {
			got := map[string]string{}
			for _, cr := range []s3x.Creds{worker, orch} {
				seed := t.prefix + "seed-" + fmt.Sprint(time.Now().UnixNano())
				must(s3x.Put(ctx, adminClient, t.bucket, seed, "seed"))
				must(s3x.Put(ctx, adminClient, "bob-fleet", "wreg/seed-src", "src"))
				got[cr.Name] = s3x.Class(o.f(ctx, s3x.Client(endpoint, cr), cr, t, seed))
				if o.kind == "bucketadmin" && got[cr.Name] == "OK" && o.name != "GetBucketPolicy" {
					got[cr.Name] += effect(ctx, t.bucket, o.name)
				}
				resetBucket(ctx, t.bucket)
			}
			exp := expectWorker(t, o)
			mark := "pass"
			if !strings.HasPrefix(got["worker"], exp) {
				mark = "**FAIL**"
				fails++
			}
			fmt.Printf("| %s | %s | %s | %s | %s | %s |\n", t.name, o.name, exp, got["worker"], got["orchestrator"], mark)
		}
	}
	// restore bucket state that orchestrator-run bucket-admin ops may have changed.
	for _, b := range []string{"bob-control", "bob-fleet", "bob-analytics", "bob-artifacts"} {
		adminClient.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(b)})
		adminClient.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(b)})
	}
	fmt.Printf("\n| global operation | expected | worker | orchestrator | |\n|---|---|---|---|---|\n")
	for _, o := range globalOps(endpoint) {
		got := map[string]string{}
		for _, cr := range []s3x.Creds{worker, orch} {
			got[cr.Name] = s3x.Class(o.f(ctx, s3x.Client(endpoint, cr), cr, target{}, ""))
		}
		exp := "DENIED"
		if o.name == "ListBuckets" {
			exp = "any"
		}
		mark := "pass"
		if strings.Contains(got["worker"], "NotImplemented") || strings.Contains(got["worker"], "http 404") || strings.Contains(got["worker"], "http 400") || strings.Contains(got["worker"], "http 503") {
			mark = "n/a on this server"
		} else if exp != "any" && !strings.HasPrefix(got["worker"], exp) {
			mark = "**FAIL**"
			fails++
		}
		fmt.Printf("| %s | %s | %s | %s | %s |\n", o.name, exp, got["worker"], got["orchestrator"], mark)
	}
	adminClient.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String("bob-evil")})
	fmt.Printf("\n%s: %d unexpected results\n", server, fails)
	return fails
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		os.Exit(2)
	}
}
