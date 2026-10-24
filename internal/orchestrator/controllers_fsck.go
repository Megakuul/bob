package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
)

// Record check of `bob-server store fsck` (R-NFR-007: "every record decodes and validates").
// The key listing is the caller's (only internal/store/sweep may LIST, R-STORE-101); this file
// reads each listed key by name through the store (dynamitedb decodes the record) and checks it.

// Problem is one record fsck found corrupt or invalid.
type Problem struct {
	Bucket, Key, Reason string
}

func (p Problem) String() string { return p.Bucket + "/" + p.Key + ": " + p.Reason }

// Fsck reads every key of bucket b (role "control" or "fleet") and checks it with CheckRecord.
// Keys under _bob/ (probe objects) are skipped. A key deleted between listing and reading is
// not a problem.
func Fsck(ctx context.Context, b *store.Bucket, role string, keys []string) ([]Problem, error) {
	var out []Problem
	for _, k := range keys {
		if strings.HasPrefix(k, "_bob/") {
			continue
		}
		switch err := CheckRecord(ctx, b, role, k); {
		case err == nil, errors.Is(err, store.ErrNotFound):
		case errors.Is(err, errCorrupt), errors.Is(err, errInvalid):
			out = append(out, Problem{b.Name(), k, err.Error()})
		default:
			return out, fmt.Errorf("fsck: %s/%s: %w", b.Name(), k, err)
		}
	}
	return out, nil
}

func oneOf(s string, set ...string) bool { return slices.Contains(set, s) }

// errCorrupt (the object does not decode) and errInvalid (it decodes but breaks a rule of its
// record type) classify CheckRecord's findings; their text is the message prefix.
var (
	errCorrupt = errors.New("corrupt")
	errInvalid = errors.New("invalid")
)

func corrupt(err error) error { return fmt.Errorf("%w: %w", errCorrupt, err) }

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errInvalid}, a...)...)
}

// read reads the record at key with store.Get; a record that does not decode is "corrupt".
func read[T any](ctx context.Context, b *store.Bucket, key *T) (*T, error) {
	v, err := store.Get(ctx, b, key)
	if err != nil && decodeFailed(err) {
		return nil, corrupt(err)
	}
	return v, err
}

// decodeFailed reports whether err is dynamitedb's CBOR decode error of a stored object.
// dynamitedb returns the decoder's error unwrapped (get.go deserialize); fxamacker/cbor v2.9.2
// also reports some malformed input with untyped errors.New("cbor: …") (valid.go: length
// overflow), which only the message prefix identifies.
func decodeFailed(err error) bool {
	var syn *cbor.SyntaxError
	var sem *cbor.SemanticError
	var typ *cbor.UnmarshalTypeError
	var tag *cbor.WrongTagError
	var lim *cbor.MaxArrayElementsError
	var pairs *cbor.MaxMapPairsError
	var depth *cbor.MaxNestedLevelError
	return errors.As(err, &syn) || errors.As(err, &sem) || errors.As(err, &typ) || errors.As(err, &tag) ||
		errors.As(err, &lim) || errors.As(err, &pairs) || errors.As(err, &depth) || errors.Is(err, store.ErrPanic) ||
		errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "cbor:")
}

// CheckRecord reads one stored object of a control or fleet bucket by its key and checks the
// invariants of its record type (the record type comes from the key's pk); objects of other
// pks must be well-formed CBOR. The error wraps errCorrupt ("corrupt: …", does not decode) or
// errInvalid ("invalid: …", decodes, breaks a rule); a missing key is store.ErrNotFound.
func CheckRecord(ctx context.Context, b *store.Bucket, role, key string) error {
	parts := strings.Split(key, "/")
	id := store.ID(parts[len(parts)-1])
	if len(parts) != 2 && len(parts) != 4 {
		return wellformed(ctx, b, key)
	}
	switch role + "/" + parts[0] {
	case "fleet/job":
		r, err := read(ctx, b, &store.JobRecord{ID: store.ID(parts[1])})
		if err != nil {
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
		r, err := read(ctx, b, &store.WorkerRecord{Wid: id})
		if err != nil {
			return err
		}
		if !oneOf(r.State.Value(), "active", "stale", "draining", "revoked", "gone") {
			return invalid("worker state %q", r.State.Value())
		}
	case "fleet/fleet":
		r, err := read(ctx, b, &store.FleetConfig{Name: id})
		if err != nil {
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
		r, err := read(ctx, b, &store.ControllerLease{Name: id})
		if err != nil {
			return err
		}
		if r.Owner.Value() != "" && r.Epoch.Value() < 1 {
			return invalid("lease held by %s with epoch 0", r.Owner.Value())
		}
	case "fleet/qs":
		if len(parts) != 4 {
			return wellformed(ctx, b, key)
		}
		r, err := read(ctx, b, &store.QueueSlot{Class: store.ID(parts[1]), Seq: store.ID(parts[3])})
		if err != nil {
			return err
		}
		if r.Job.Value() == "" {
			return invalid("queue slot without job")
		}
	case "fleet/orch":
		_, err := read(ctx, b, &store.OrchRecord{ID: id})
		return err
	case "fleet/whb":
		_, err := read(ctx, b, &store.Heartbeat{Wid: id})
		return err
	case "control/build":
		r, err := read(ctx, b, &store.BuildRecord{ID: id})
		if err != nil {
			return err
		}
		if !oneOf(r.State.Value(), "pending", "queued", "running", "succeeded", "failed", "cancelled", "superseded", "skipped") {
			return invalid("build state %q", r.State.Value())
		}
		if len(r.Jobs.Value()) > store.MaxBuildJobs {
			return invalid("%d jobs (limit %d)", len(r.Jobs.Value()), store.MaxBuildJobs)
		}
	case "control/okey":
		r, err := read(ctx, b, &OrchKeyRecord{Kid: id})
		if err != nil {
			return err
		}
		if len(r.Seed.Value()) != 32 {
			return invalid("key seed of %d bytes", len(r.Seed.Value()))
		}
	case "control/user":
		_, err := read(ctx, b, &store.UserRecord{ID: id})
		return err
	case "control/role":
		_, err := read(ctx, b, &store.RoleRecord{ID: id})
		return err
	case "control/sa":
		_, err := read(ctx, b, &store.ServiceAccountRecord{ID: id})
		return err
	default:
		return wellformed(ctx, b, key)
	}
	return nil
}

// wellformed checks an object of a pk without a typed check: it must be well-formed CBOR.
func wellformed(ctx context.Context, b *store.Bucket, key string) error {
	body, err := store.ReadRaw(ctx, b, key)
	if err != nil {
		return err
	}
	if err := cbor.Wellformed(body); err != nil {
		return corrupt(err)
	}
	return nil
}
