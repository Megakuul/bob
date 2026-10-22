package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/sweep"
)

// StagingSweep is the hourly staging sweep controller (R-STORE-105, R-STORE-101): it installs
// the 7-day lifecycle rule on staging/ once, and lists staging/ to delete objects older than
// 7 days (the only LIST of the artifacts bucket outside the lake sweep).
type StagingSweep struct {
	Sweep *sweep.Staging
	Log   *slog.Logger

	lifecycle bool
}

// Pass runs one sweep.
func (x *StagingSweep) Pass(ctx context.Context, r *Run) error {
	log := x.Log
	if log == nil {
		log = slog.Default()
	}
	if !x.lifecycle {
		applied, err := x.Sweep.EnsureLifecycle(ctx)
		if err != nil {
			log.Warn("staging lifecycle rule not installed", "err", err.Error())
		} else {
			x.lifecycle = true
			log.Info("staging lifecycle rule", "applied", applied)
		}
	}
	n, err := x.Sweep.Sweep(ctx, r.Now, sweep.StagingMaxAge)
	if n > 0 {
		log.Info("staging sweep", "deleted", n)
	}
	return err
}

// Controller returns the staging sweep controller (hourly, lease ctl/staging-sweep).
func (x *StagingSweep) Controller(fleet *store.Bucket, owner string, skew time.Duration, now func() time.Time) *Controller {
	return &Controller{ID: CtlStagingSweep, Fleet: fleet, Owner: owner, Interval: time.Hour, Skew: skew,
		Now: now, Persist: true, Pass: x.Pass, Log: x.Log}
}

// Limits of a COMPLETE.json (§6.6.4).
const (
	MaxComplete      = 4 << 20
	maxCompletePaths = 100_000
)

// completeDoc is the part of COMPLETE.json that names staged objects (§6.6.4).
type completeDoc struct {
	V      int      `json:"v"`
	Paths  []string `json:"paths"`
	Images []struct {
		Manifest string   `json:"manifest"`
		Blobs    []string `json:"blobs"`
	} `json:"images"`
	Assets []struct {
		File string `json:"file"`
	} `json:"assets"`
}

var (
	stagingPrefixRE = regexp.MustCompile(`^staging/[A-Za-z0-9._-]{1,100}/[A-Za-z0-9._-]{1,100}/[0-9]{1,10}/$`)
	storePathRE     = regexp.MustCompile(`^/nix/store/([0-9a-df-np-sv-z]{32})-[^/]{1,211}$`)
	digestRE        = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)
	assetRE         = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,255}$`)
)

// StagedKeys returns the artifact keys of a staged job attempt that COMPLETE.json names
// (R-STORE-105: deleted by known key after promotion): NARs and narinfos of the paths, OCI
// blobs and manifests, assets when withAssets (a release copies them first), and
// COMPLETE.json itself last. COMPLETE.json is worker input: every name is validated.
func StagedKeys(prefix string, complete []byte, withAssets bool) ([]string, error) {
	if !stagingPrefixRE.MatchString(prefix) {
		return nil, fmt.Errorf("orchestrator: %q is not a staging prefix", prefix)
	}
	if len(complete) > MaxComplete {
		return nil, fmt.Errorf("orchestrator: COMPLETE.json larger than %d bytes", MaxComplete)
	}
	var d completeDoc
	if err := json.Unmarshal(complete, &d); err != nil {
		return nil, fmt.Errorf("orchestrator: COMPLETE.json: %w", err)
	}
	if len(d.Paths) > maxCompletePaths {
		return nil, fmt.Errorf("orchestrator: COMPLETE.json lists more than %d paths", maxCompletePaths)
	}
	var keys []string
	for _, p := range d.Paths {
		m := storePathRE.FindStringSubmatch(p)
		if m == nil {
			return nil, fmt.Errorf("orchestrator: COMPLETE.json: invalid store path %q", p)
		}
		keys = append(keys, prefix+"nar/"+m[1]+".nar.zst", prefix+"narinfo/"+m[1]+".narinfo")
	}
	for _, img := range d.Images {
		for _, dg := range append([]string{img.Manifest}, img.Blobs...) {
			m := digestRE.FindStringSubmatch(dg)
			if m == nil {
				return nil, fmt.Errorf("orchestrator: COMPLETE.json: invalid digest %q", dg)
			}
			keys = append(keys, prefix+"oci/blobs/sha256/"+m[1])
		}
	}
	if withAssets {
		for _, a := range d.Assets {
			if !assetRE.MatchString(a.File) || a.File == "." || a.File == ".." {
				return nil, fmt.Errorf("orchestrator: COMPLETE.json: invalid asset name %q", a.File)
			}
			keys = append(keys, prefix+"assets/"+a.File)
		}
	}
	return append(keys, path.Join(prefix, "COMPLETE.json")), nil
}

// Staged deletes promoted staging objects of the artifacts bucket by known key.
type Staged struct {
	S3     *s3.Client
	Bucket string
}

// Delete deletes the keys StagedKeys names; COMPLETE.json goes last, so a crash in between
// leaves a COMPLETE.json whose objects a re-run deletes again (deleting a missing key is a
// no-op), and anything else expires with the 7-day rule or sweep.
func (x *Staged) Delete(ctx context.Context, prefix string, complete []byte, withAssets bool) (int, error) {
	keys, err := StagedKeys(prefix, complete, withAssets)
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for i, k := range keys {
		if i == len(keys)-1 && len(errs) > 0 {
			break // keep COMPLETE.json while objects it names remain
		}
		if _, err := x.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(x.Bucket), Key: aws.String(k)}); err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("delete %s: %w", k, err))
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, fmt.Errorf("orchestrator: staging %s: %w", strings.TrimSuffix(prefix, "/"), errors.Join(errs...))
	}
	return n, nil
}
