package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"bobspike/s11/internal/s3x"
)

type assumeRoleResp struct {
	Result struct {
		Credentials struct {
			AccessKeyId, SecretAccessKey, SessionToken, Expiration string
		}
	} `xml:"AssumeRoleResult"`
}

// sessionPolicy narrows a worker's temporary credentials to its own prefixes.
func sessionPolicy(wid string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::bob-fleet","arn:aws:s3:::bob-analytics"]},
{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bob-fleet/*","arn:aws:s3:::bob-artifacts/*","arn:aws:s3:::bob-analytics/*"]},
{"Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::bob-fleet/wreg/%[1]s","arn:aws:s3:::bob-fleet/whb/%[1]s","arn:aws:s3:::bob-fleet/lease/*","arn:aws:s3:::bob-fleet/jstat/*","arn:aws:s3:::bob-analytics/*","arn:aws:s3:::bob-artifacts/staging/%[1]s/*"]},
{"Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::bob-control/*"]}
]}`, wid) // last statement: session policy tries to widen beyond the parent; must have no effect
}

// assumeRole calls STS AssumeRole on the S3 endpoint.
func assumeRole(ctx context.Context, endpoint string, caller s3x.Creds, roleArn, policy string, dur int) (s3x.Creds, time.Time, error) {
	form := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleSessionName": {"bob-worker"}, "DurationSeconds": {strconv.Itoa(dur)}}
	if roleArn != "" {
		form.Set("RoleArn", roleArn)
	}
	if policy != "" {
		form.Set("Policy", policy)
	}
	body := form.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := signedDo(ctx, req, []byte(body), caller, "sts")
	if err != nil {
		return s3x.Creds{}, time.Time{}, err
	}
	var ar assumeRoleResp
	if err := xml.Unmarshal(resp, &ar); err != nil {
		return s3x.Creds{}, time.Time{}, fmt.Errorf("parse: %v: %s", err, snippet(resp))
	}
	c := ar.Result.Credentials
	exp, _ := time.Parse(time.RFC3339, c.Expiration)
	if c.AccessKeyId == "" {
		return s3x.Creds{}, time.Time{}, fmt.Errorf("no credentials in %s", snippet(resp))
	}
	return s3x.Creds{Name: "sts", AK: c.AccessKeyId, SK: c.SecretAccessKey, Session: c.SessionToken}, exp, nil
}

func signedDo(ctx context.Context, req *http.Request, body []byte, cr s3x.Creds, service string) ([]byte, error) {
	var out []byte
	err := doSignedResp(ctx, req, body, cr, service, &out)
	return out, err
}

func doSignedResp(ctx context.Context, req *http.Request, body []byte, cr s3x.Creds, service string, out *[]byte) error {
	// reuse the signing in matrix.go, but keep the body.
	rec := &captureTransport{}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rec
	err := doSigned(ctx, req, body, cr, service)
	http.DefaultClient.Transport = old
	*out = rec.body
	return err
}

type captureTransport struct{ body []byte }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.body = b
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, nil
}

func runSTS(ctx context.Context, server, endpoint string, root, worker, orch s3x.Creds) int {
	caller := creds("sts-caller", "S11_STSCALLER")
	roleArn := os.Getenv("S11_ROLEARN")
	c := &checker{server: server}
	fmt.Printf("\n### %s: short-lived S3 credentials (STS AssumeRole)\n\n| check | detail | |\n|---|---|---|\n", server)

	t0 := time.Now()
	tc, exp, err := assumeRole(ctx, endpoint, caller, roleArn, sessionPolicy("wtest1"), 900)
	tAR := time.Since(t0)
	c.check("orchestrator mints 900 s credentials with session policy", err == nil, fmt.Sprintf("err=%v expires in %s; call %s", err, time.Until(exp).Round(time.Second), ms(tAR)))
	if err != nil {
		fmt.Printf("\n%s sts: %d failed checks\n", server, c.fails+1)
		return 1
	}
	cl := s3x.Client(endpoint, tc)
	probe := func(name, want string, err error) {
		got := s3x.Class(err)
		ok := strings.HasPrefix(got, want)
		if want == "REFUSED" {
			ok = got != "OK"
		}
		c.check(name, ok, got)
	}
	probe("temp: PUT artifacts/staging/wtest1/x", "OK", s3x.Put(ctx, cl, "bob-artifacts", "staging/wtest1/x", "x"))
	probe("temp: PUT artifacts/staging/wother/x (session policy)", "DENIED", s3x.Put(ctx, cl, "bob-artifacts", "staging/wother/x", "x"))
	probe("temp: PUT fleet/wreg/wother (session policy)", "DENIED", s3x.Put(ctx, cl, "bob-fleet", "wreg/wother", "x"))
	probe("temp: PUT fleet/whb/wtest1", "OK", s3x.Put(ctx, cl, "bob-fleet", "whb/wtest1", "x"))
	probe("temp: PUT fleet/worker/wtest1 (parent policy)", "DENIED", s3x.Put(ctx, cl, "bob-fleet", "worker/wtest1", "x"))
	probe("temp: PUT analytics/logs/x", "OK", s3x.Put(ctx, cl, "bob-analytics", "logs/x", "x"))
	probe("temp: PUT control/* (session policy tries to widen)", "DENIED", s3x.Put(ctx, cl, "bob-control", "user/x", "x"))
	probe("temp: GET control/*", "DENIED", get(ctx, cl, "bob-control", "user/secret"))
	_, err = cl.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: strp("bob-control")})
	probe("temp: LIST control", "DENIED", err)
	bad := tc
	bad.Session = tc.Session[:len(tc.Session)-4] + "AAAA"
	probe("tampered session token", "REFUSED", s3x.Put(ctx, s3x.Client(endpoint, bad), "bob-analytics", "logs/y", "x"))
	nos := tc
	nos.Session = ""
	probe("temp key without session token", "REFUSED", s3x.Put(ctx, s3x.Client(endpoint, nos), "bob-analytics", "logs/y", "x"))
	_, _, err = assumeRole(ctx, endpoint, tc, roleArn, "", 900)
	probe("temp creds call AssumeRole again (chaining)", "DENIED", err)
	_, _, err = assumeRole(ctx, endpoint, worker, roleArn, "", 900)
	probe("worker (fleet) credential calls AssumeRole", "DENIED", err)
	_, e60, err := assumeRole(ctx, endpoint, caller, roleArn, sessionPolicy("wtest1"), 60)
	c.check("DurationSeconds=60 (below 900 minimum)", true, fmt.Sprintf("class=%s expires in %s", s3x.Class(err), time.Until(e60).Round(time.Second)))
	_, e2, err := assumeRole(ctx, endpoint, caller, roleArn, sessionPolicy("wtest1"), 86400)
	c.check("DurationSeconds=86400 (above max)", true, fmt.Sprintf("class=%s expires in %s", s3x.Class(err), time.Until(e2).Round(time.Second)))
	// save for the expiry test
	os.WriteFile(os.Getenv("S11_STSOUT"), []byte(tc.AK+"\n"+tc.SK+"\n"+tc.Session+"\n"+exp.Format(time.RFC3339)+"\n"), 0o600)
	fmt.Printf("\n%s sts: %d failed checks\n", server, c.fails)
	return c.fails
}

// runSTSCheck uses saved temp credentials once (for the expiry / revocation tests).
func runSTSCheck(ctx context.Context, server, endpoint, file string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	l := strings.Split(string(b), "\n")
	tc := s3x.Creds{AK: l[0], SK: l[1], Session: l[2]}
	err = s3x.Put(ctx, s3x.Client(endpoint, tc), "bob-analytics", "logs/sts-check", "x")
	fmt.Printf("| %s | temp creds (exp %s) used at %s | %s |\n", server, l[3], time.Now().UTC().Format(time.RFC3339), s3x.Class(err))
	return 0
}

func strp(s string) *string { return &s }
