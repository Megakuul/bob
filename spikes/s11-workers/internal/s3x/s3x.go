// Package s3x has small S3 helpers shared by the S11 spike commands.
package s3x

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// Creds is one S3 identity.
type Creds struct {
	Name, AK, SK, Session string
}

// Client builds a path-style S3 client for endpoint with static credentials.
func Client(endpoint string, c Creds) *s3.Client {
	return s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(c.AK, c.SK, c.Session),
		HTTPClient:   &http.Client{Timeout: 20 * time.Second},
		// Some servers reject the newer default CRC32 trailers on some calls; keep it plain.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// Class turns an S3 error into OK / DENIED / NOTFOUND / PRECOND / ERR:<code>.
func Class(err error) string {
	if err == nil {
		return "OK"
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AccessDenied", "Forbidden", "AllAccessDisabled", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken":
			if ae.ErrorCode() == "AccessDenied" || ae.ErrorCode() == "Forbidden" {
				return "DENIED"
			}
			return "DENIED(" + ae.ErrorCode() + ")"
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return "NOTFOUND"
		case "PreconditionFailed":
			return "PRECOND"
		}
		return "ERR:" + ae.ErrorCode()
	}
	s := err.Error()
	if strings.Contains(s, "StatusCode: 403") {
		return "DENIED"
	}
	return "ERR:" + s
}

// Put writes a small object (used to seed test state with an admin client).
func Put(ctx context.Context, c *s3.Client, bucket, key, body string) error {
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: strings.NewReader(body)})
	return err
}
