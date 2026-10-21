package logstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// lake has no delete/retention API. This is the local workaround: edit lake's catalog
// object (format of lake/internal/catalog at HEAD) with a conditional PUT, then delete the
// dropped shard objects. It depends on lake's internal on-disk format.

// catalog mirrors lake/internal/catalog.Catalog, keeping unknown fields intact.
type rawCatalog struct {
	Version string                     `json:"version"`
	Tables  map[string]json.RawMessage `json:"tables"`
}

type rawTable struct {
	Shards []json.RawMessage `json:"shards"`
}

type shardHead struct {
	Size   int    `json:"size"`
	Target string `json:"target"`
	Ranges map[string]struct {
		MaxInt    int64  `json:"max_int"`
		MaxString string `json:"max_string"`
	} `json:"ranges"`
}

// DropResult reports one retention run.
type DropResult struct {
	Dropped, Kept int
	Bytes         int64
	Conflict      bool
}

// DropShards removes every shard of table for which drop(shard) is true. The catalog is
// updated with If-Match first, the objects are deleted afterwards (a crash between the two
// leaves orphans, never dangling catalog entries).
func DropShards(ctx context.Context, cl *s3.Client, bucket, table string, drop func(target string, maxInt map[string]int64, maxStr map[string]string) bool) (DropResult, error) {
	var res DropResult
	key := aws.String("lakedb.catalog")
	o, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: key})
	if err != nil {
		return res, err
	}
	data, err := io.ReadAll(o.Body)
	o.Body.Close()
	if err != nil {
		return res, err
	}
	var c rawCatalog
	if err := json.Unmarshal(data, &c); err != nil {
		return res, err
	}
	if c.Version != "v1" {
		return res, fmt.Errorf("unknown lake catalog version %q", c.Version)
	}
	var t rawTable
	if raw, ok := c.Tables[table]; ok {
		if err := json.Unmarshal(raw, &t); err != nil {
			return res, err
		}
	}
	var keep []json.RawMessage
	var gone []string
	for _, s := range t.Shards {
		var h shardHead
		if err := json.Unmarshal(s, &h); err != nil {
			return res, err
		}
		mi, ms := map[string]int64{}, map[string]string{}
		for col, r := range h.Ranges {
			mi[col], ms[col] = r.MaxInt, r.MaxString
		}
		if drop(h.Target, mi, ms) {
			gone = append(gone, h.Target)
			res.Bytes += int64(h.Size)
		} else {
			keep = append(keep, s)
		}
	}
	res.Dropped, res.Kept = len(gone), len(keep)
	if len(gone) == 0 {
		return res, nil
	}
	if keep == nil {
		keep = []json.RawMessage{}
	}
	t.Shards = keep
	rt, _ := json.Marshal(t)
	c.Tables[table] = rt
	out, _ := json.Marshal(c)
	if _, err := cl.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: key, IfMatch: o.ETag, Body: bytes.NewReader(out)}); err != nil {
		res.Conflict = true
		return res, fmt.Errorf("catalog update: %w", err)
	}
	for _, g := range gone {
		if _, err := cl.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(g)}); err != nil {
			return res, fmt.Errorf("delete %s: %w", g, err)
		}
	}
	return res, nil
}
