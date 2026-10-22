package policy

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/parity"
)

// stmt is one identity-policy statement of SPEC §05.2.3.
type stmt struct {
	deny      bool
	actions   []string // "s3:GetObject" or "s3:*"
	resources []string // "bucket" (the bucket itself) or "bucket/prefix*" (objects; * is a suffix wildcard)
}

const (
	control   = "bob-control"
	fleet     = "bob-fleet"
	analytics = "bob-analytics"
	artifacts = "bob-artifacts"
)

// workerPolicy is the worker table of SPEC §05.2.3.
var workerPolicy = []stmt{
	{deny: true, actions: []string{"s3:*"}, resources: []string{control, control + "/*"}},
	{actions: []string{"s3:ListBucket"}, resources: []string{fleet}},
	{actions: []string{"s3:GetObject"}, resources: []string{fleet + "/*"}},
	{actions: []string{"s3:PutObject"}, resources: []string{
		fleet + "/wreg/*", fleet + "/wrs/*", fleet + "/wrt/*", fleet + "/whb/*", fleet + "/job/*", fleet + "/qs/*", fleet + "/qh/*"}},
	{actions: []string{"s3:ListBucket"}, resources: []string{analytics}},
	{actions: []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}, resources: []string{analytics + "/*"}},
	{actions: []string{"s3:GetObject"}, resources: []string{artifacts + "/*"}},
	{actions: []string{"s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}, resources: []string{artifacts + "/staging/*"}},
}

// orchestratorPolicy is "s3:* on all four buckets".
var orchestratorPolicy = []stmt{
	{actions: []string{"s3:*"}, resources: []string{
		control, control + "/*", fleet, fleet + "/*", analytics, analytics + "/*", artifacts, artifacts + "/*"}},
}

func matchRes(pattern, res string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(res, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == res
}

func matchAct(pattern, act string) bool { return pattern == "s3:*" || pattern == act }

// need is one (action, resource) pair an operation requires.
type need struct{ action, resource string }

// allowed reports whether every need is granted and none is explicitly denied.
func allowed(p []stmt, needs []need) bool {
	for _, n := range needs {
		granted := false
		for _, s := range p {
			hit := false
			for _, a := range s.actions {
				if matchAct(a, n.action) {
					for _, r := range s.resources {
						if matchRes(r, n.resource) {
							hit = true
						}
					}
				}
			}
			if !hit {
				continue
			}
			if s.deny {
				return false
			}
			granted = true
		}
		if !granted {
			return false
		}
	}
	return true
}

// target is one of the six bucket/prefix targets of the matrix.
type target struct {
	name   string
	bucket string
	prefix string // object key prefix of the target ("" = whole bucket)
}

var targets = []target{
	{"bob-control", control, "user/"},
	{"bob-fleet worker/ (orchestrator-owned)", fleet, "worker/"},
	{"bob-fleet wreg/", fleet, "wreg/"},
	{"bob-analytics", analytics, "log_line/"},
	{"bob-artifacts nix/nar/ (non-staging)", artifacts, "nix/nar/"},
	{"bob-artifacts staging/", artifacts, "staging/b1/j1/1/"},
}

// copyDst is a place the worker may write to, used as the destination of "copy out".
var copyDst = need{"s3:PutObject", fleet + "/job/copy-dst"}

// op is one of the 27 S3 operations; needs says what IAM requires for it on target tg
// (objects are tg.prefix+"obj").
type op struct {
	name  string
	needs func(tg target) []need
}

func obj(tg target) string { return tg.bucket + "/" + tg.prefix + "obj" }

func bucketNeed(action string) func(target) []need {
	return func(tg target) []need { return []need{{action, tg.bucket}} }
}

func objNeed(actions ...string) func(target) []need {
	return func(tg target) []need {
		var n []need
		for _, a := range actions {
			n = append(n, need{a, obj(tg)})
		}
		return n
	}
}

// readableSrc is an object every identity may read (copy source for "copy in"): the harness
// seeds fleet/job/src for it.
const readableSrcKey = "job/src"

var ops = []op{
	{"HeadBucket", bucketNeed("s3:ListBucket")},
	{"ListObjectsV2", bucketNeed("s3:ListBucket")},
	{"ListObjectVersions", bucketNeed("s3:ListBucketVersions")},
	{"ListMultipartUploads", bucketNeed("s3:ListBucketMultipartUploads")},
	{"GetObject", objNeed("s3:GetObject")},
	{"HeadObject", objNeed("s3:GetObject")},
	{"GetObject (ranged)", objNeed("s3:GetObject")},
	{"GetObjectAcl", objNeed("s3:GetObjectAcl")},
	{"GetObjectTagging", objNeed("s3:GetObjectTagging")},
	{"GET (presigned)", objNeed("s3:GetObject")},
	{"PutObject (new)", objNeed("s3:PutObject")},
	{"PutObject (overwrite)", objNeed("s3:PutObject")},
	{"PutObject (If-None-Match)", objNeed("s3:PutObject")},
	{"PUT (presigned)", objNeed("s3:PutObject")},
	{"multipart upload", objNeed("s3:PutObject")},
	{"PutObjectTagging", objNeed("s3:PutObjectTagging")},
	{"PutObjectAcl", objNeed("s3:PutObjectAcl")},
	{"CopyObject into target", func(tg target) []need {
		return []need{{"s3:GetObject", fleet + "/" + readableSrcKey}, {"s3:PutObject", obj(tg)}}
	}},
	{"CopyObject out of target", func(tg target) []need {
		return []need{{"s3:GetObject", obj(tg)}, copyDst}
	}},
	{"UploadPartCopy out of target", func(tg target) []need {
		return []need{{"s3:GetObject", obj(tg)}, copyDst}
	}},
	{"DeleteObject", objNeed("s3:DeleteObject")},
	{"DeleteObjects", objNeed("s3:DeleteObject")},
	{"GetBucketPolicy", bucketNeed("s3:GetBucketPolicy")},
	{"PutBucketPolicy", bucketNeed("s3:PutBucketPolicy")},
	{"PutBucketVersioning", bucketNeed("s3:PutBucketVersioning")},
	{"PutBucketLifecycle", bucketNeed("s3:PutLifecycleConfiguration")},
	{"PutBucketAcl", bucketNeed("s3:PutBucketAcl")},
}

// expect is the matrix cell: whether policy p lets op o through on target tg. The control
// bucket policy additionally denies every non-orchestrator identity on bob-control (both
// layers are checked by callers through workerPolicy's explicit Deny and the bucket policy).
func expect(p []stmt, o op, tg target) bool { return allowed(p, o.needs(tg)) }

// TestModelAgainstS11Table pins the reference evaluator to the worker-outcome table of S11 §3.1
// (docs/spikes/s11-workers.md), so the matrix never compares the server with a wrong model.
func TestModelAgainstS11Table(t *testing.T) {
	parity.Covers(t, "R-STORE-002")
	byName := map[string]op{}
	for _, o := range ops {
		byName[o.name] = o
	}
	if len(ops) != 27 || len(targets) != 6 {
		t.Fatalf("matrix is %d ops × %d targets, want 27 × 6", len(ops), len(targets))
	}
	for _, o := range ops { // control: nothing, all 27 refused
		if expect(workerPolicy, o, targets[0]) {
			t.Errorf("worker may %s on bob-control", o.name)
		}
	}
	type row struct {
		tg      target
		allowed []string
	}
	for _, r := range []row{
		{targets[1], []string{"ListObjectsV2", "HeadBucket", "GetObject", "HeadObject", "GetObject (ranged)", "GET (presigned)", "CopyObject out of target", "UploadPartCopy out of target"}},
		{targets[2], []string{"ListObjectsV2", "HeadBucket", "GetObject", "HeadObject", "GetObject (ranged)", "GET (presigned)", "CopyObject out of target", "UploadPartCopy out of target", "PutObject (new)", "PutObject (overwrite)", "PutObject (If-None-Match)", "PUT (presigned)", "multipart upload", "CopyObject into target"}},
		{targets[3], []string{"ListObjectsV2", "HeadBucket", "GetObject", "HeadObject", "GetObject (ranged)", "GET (presigned)", "CopyObject out of target", "UploadPartCopy out of target", "PutObject (new)", "PutObject (overwrite)", "PutObject (If-None-Match)", "PUT (presigned)", "multipart upload", "CopyObject into target", "DeleteObject", "DeleteObjects"}},
		{targets[4], []string{"GetObject", "HeadObject", "GetObject (ranged)", "GET (presigned)", "CopyObject out of target", "UploadPartCopy out of target"}},
		{targets[5], []string{"GetObject", "HeadObject", "GetObject (ranged)", "GET (presigned)", "CopyObject out of target", "UploadPartCopy out of target", "PutObject (new)", "PutObject (overwrite)", "PutObject (If-None-Match)", "PUT (presigned)", "multipart upload", "CopyObject into target"}},
	} {
		want := map[string]bool{}
		for _, n := range r.allowed {
			if _, ok := byName[n]; !ok {
				t.Fatalf("unknown op %q in the test table", n)
			}
			want[n] = true
		}
		for _, o := range ops {
			if got := expect(workerPolicy, o, r.tg); got != want[o.name] {
				t.Errorf("model: worker %s on %s = %v, S11 table says %v", o.name, r.tg.name, got, want[o.name])
			}
		}
	}
	for _, o := range ops { // orchestrator: s3:* everywhere it works
		for _, tg := range targets {
			if !expect(orchestratorPolicy, o, tg) {
				t.Errorf("model: orchestrator is refused %s on %s", o.name, tg.name)
			}
		}
	}
}
