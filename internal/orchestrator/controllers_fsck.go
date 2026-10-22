package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
)

// Record check of `bob-server store fsck` (R-NFR-007: "every record decodes and validates").
// The key listing is the caller's (only internal/store/sweep may LIST, R-STORE-101); this file
// reads each listed key by name and checks it.

// maxRecord bounds one record read by fsck (the store's own cap).
const maxRecord = 8 << 20

// Problem is one record fsck found corrupt or invalid.
type Problem struct {
	Bucket, Key, Reason string
}

func (p Problem) String() string { return p.Bucket + "/" + p.Key + ": " + p.Reason }

// Fsck reads every key of bucket (role "control" or "fleet") and checks it with CheckRecord.
// Keys under _bob/ (probe objects) are skipped. A key deleted between listing and reading is
// not a problem.
func Fsck(ctx context.Context, client *s3.Client, bucket, role string, keys []string) ([]Problem, error) {
	var out []Problem
	for _, k := range keys {
		if strings.HasPrefix(k, "_bob/") {
			continue
		}
		o, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
		if err != nil {
			var nf interface{ ErrorCode() string }
			if errors.As(err, &nf) && (nf.ErrorCode() == "NoSuchKey" || nf.ErrorCode() == "NotFound") {
				continue
			}
			return out, fmt.Errorf("fsck: get %s/%s: %w", bucket, k, err)
		}
		body, err := io.ReadAll(io.LimitReader(o.Body, maxRecord+1))
		_ = o.Body.Close()
		if err != nil {
			return out, fmt.Errorf("fsck: read %s/%s: %w", bucket, k, err)
		}
		if len(body) > maxRecord {
			out = append(out, Problem{bucket, k, "corrupt: larger than 8 MiB"})
			continue
		}
		if err := CheckRecord(role, k, body); err != nil {
			out = append(out, Problem{bucket, k, err.Error()})
		}
	}
	return out, nil
}

func oneOf(s string, set ...string) bool { return slices.Contains(set, s) }

func corrupt(err error) error { return fmt.Errorf("corrupt: %w", err) }

func invalid(format string, a ...any) error { return fmt.Errorf("invalid: "+format, a...) }

func decodeInto(body []byte, v any) error {
	if err := cbor.Unmarshal(body, v); err != nil {
		return corrupt(err)
	}
	return nil
}

// CheckRecord decodes one stored object of a control or fleet bucket by its key's pk and
// checks the invariants of its record type; objects of other pks must be well-formed CBOR.
// The error starts with "corrupt:" (does not decode) or "invalid:" (decodes, breaks a rule).
func CheckRecord(role, key string, body []byte) error {
	pk, _, _ := strings.Cut(key, "/")
	switch role + "/" + pk {
	case "fleet/job":
		var r store.JobRecord
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		if !oneOf(r.State.Value(), StateQueued, StateClaimed, StateRunning, StateSucceeded, StateFailed, StateCancelled) {
			return invalid("job state %q", r.State.Value())
		}
		if (r.State.Value() == StateClaimed || r.State.Value() == StateRunning) && (r.Owner.Value() == "" || r.Epoch.Value() < 1) {
			return invalid("job %s without owner or epoch", r.State.Value())
		}
		if len(r.Spec.Value()) == 0 || len(r.Spec.Value()) > store.MaxJobSpec {
			return invalid("job spec of %d bytes", len(r.Spec.Value()))
		}
	case "fleet/worker":
		var r store.WorkerRecord
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		if !oneOf(r.State.Value(), "active", "stale", "draining", "revoked", "gone") {
			return invalid("worker state %q", r.State.Value())
		}
	case "fleet/fleet":
		var r store.FleetConfig
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, m := range r.Members.Value() {
			if m.Orch == "" || seen[m.Orch] {
				return invalid("member %q empty or listed twice", m.Orch)
			}
			seen[m.Orch] = true
		}
		if len(r.Classes.Value()) > 256 {
			return invalid("%d classes (limit 256)", len(r.Classes.Value()))
		}
	case "fleet/ctl":
		var r store.ControllerLease
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		if r.Owner.Value() != "" && r.Epoch.Value() < 1 {
			return invalid("lease held by %s with epoch 0", r.Owner.Value())
		}
	case "fleet/qs":
		var r store.QueueSlot
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		if r.Job.Value() == "" {
			return invalid("queue slot without job")
		}
	case "fleet/orch":
		var r store.OrchRecord
		return decodeInto(body, &r)
	case "fleet/whb":
		var r store.Heartbeat
		return decodeInto(body, &r)
	case "control/build":
		var r store.BuildRecord
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		if !oneOf(r.State.Value(), "pending", "queued", "running", "succeeded", "failed", "cancelled", "superseded", "skipped") {
			return invalid("build state %q", r.State.Value())
		}
		if len(r.Jobs.Value()) > store.MaxBuildJobs {
			return invalid("%d jobs (limit %d)", len(r.Jobs.Value()), store.MaxBuildJobs)
		}
	case "control/okey":
		var r OrchKeyRecord
		if err := decodeInto(body, &r); err != nil {
			return err
		}
		if len(r.Seed.Value()) != 32 {
			return invalid("key seed of %d bytes", len(r.Seed.Value()))
		}
	case "control/user":
		var r store.UserRecord
		return decodeInto(body, &r)
	case "control/role":
		var r store.RoleRecord
		return decodeInto(body, &r)
	case "control/sa":
		var r store.ServiceAccountRecord
		return decodeInto(body, &r)
	default:
		if err := cbor.Wellformed(body); err != nil {
			return corrupt(err)
		}
	}
	return nil
}
