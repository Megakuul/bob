package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"bobspike/s11/internal/s3x"
)

// runBpol checks how a bucket policy on the control bucket interacts with the
// worker's identity policy (explicit Deny on bob-control) and with anonymous access.
func runBpol(ctx context.Context, server, endpoint string, root, worker, orch s3x.Creds) int {
	ad := s3x.Client(endpoint, root)
	b := "bob-control"
	must(s3x.Put(ctx, ad, b, "user/secret", "secret"))
	cases := []struct{ name, policy string }{
		{"none", ""},
		{"Allow Principal:* s3:* (misconfiguration)", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:*"],"Resource":["arn:aws:s3:::bob-control","arn:aws:s3:::bob-control/*"]}]}`},
		{"Allow Principal:{AWS:[wkr]} s3:GetObject", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["wkr"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bob-control/*"]}]}`},
		{"Deny all except orch (aws:username StringNotEquals)", `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":["s3:*"],"Resource":["arn:aws:s3:::bob-control","arn:aws:s3:::bob-control/*"],"Condition":{"StringNotEquals":{"aws:username":["orch","` + root.AK + `"]}}}]}`},
	}
	fmt.Printf("\n### %s: bucket policy on bob-control vs worker identity Deny\n\n| bucket policy | PutBucketPolicy result | worker GET | worker PUT | anonymous GET | anonymous PUT | orch GET | wide GET | wide PUT |\n|---|---|---|---|---|---|---|---|---|\n", server)
	for _, c := range cases {
		res := "-"
		if c.policy != "" {
			_, err := ad.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: &b, Policy: aws.String(c.policy)})
			res = s3x.Class(err)
		}
		wc, oc := s3x.Client(endpoint, worker), s3x.Client(endpoint, orch)
		wg := s3x.Class(get(ctx, wc, b, "user/secret"))
		wp := s3x.Class(s3x.Put(ctx, wc, b, "user/by-worker", "x"))
		ag := s3x.Class(httpStatus(http.Get(endpoint + "/" + b + "/user/secret")))
		req, _ := http.NewRequest(http.MethodPut, endpoint+"/"+b+"/user/by-anon", strings.NewReader("x"))
		ap := s3x.Class(httpStatus(http.DefaultClient.Do(req)))
		og := s3x.Class(get(ctx, oc, b, "user/secret"))
		wide := s3x.Client(endpoint, creds("wide", "S11_WIDE"))
		xg := s3x.Class(get(ctx, wide, b, "user/secret"))
		xp := s3x.Class(s3x.Put(ctx, wide, b, "user/by-wide", "x"))
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", c.name, res, wg, wp, ag, ap, og, xg, xp)
		ad.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: &b})
	}
	return 0
}

func get(ctx context.Context, c *s3.Client, b, k string) error {
	r, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &b, Key: &k})
	if err == nil {
		r.Body.Close()
	}
	return err
}
