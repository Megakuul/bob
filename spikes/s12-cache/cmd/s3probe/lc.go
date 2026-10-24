package main

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var typesLC = types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{
	ID: aws.String("staging-expiry"), Status: types.ExpirationStatusEnabled,
	Filter:     &types.LifecycleRuleFilter{Prefix: aws.String("staging/")},
	Expiration: &types.LifecycleExpiration{Days: aws.Int32(7)},
	AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(1)},
}}}
