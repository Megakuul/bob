package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// StorageConfig is the storage.* configuration of bob-server (SPEC §05.11.1). The four
// buckets live behind one endpoint; credentials come from the AWS SDK default chain (D57).
type StorageConfig struct {
	Endpoint, Region                     string
	Control, Fleet, Analytics, Artifacts string
	// WorkerProbeCredential is "<access key>:<secret key>" of a worker credential, or "" (R-SEC-102).
	WorkerProbeCredential string
}

// StorageConfigFromEnv reads BOB_STORAGE_* (BOB_<UPPER_SNAKE> of the storage.* keys) with the
// default bucket names.
func StorageConfigFromEnv(getenv func(string) string) (StorageConfig, error) {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	c := StorageConfig{
		Endpoint:              getenv("BOB_STORAGE_ENDPOINT"),
		Region:                getenv("BOB_STORAGE_REGION"),
		Control:               or("BOB_STORAGE_BUCKETS_CONTROL", "bob-control"),
		Fleet:                 or("BOB_STORAGE_BUCKETS_FLEET", "bob-fleet"),
		Analytics:             or("BOB_STORAGE_BUCKETS_ANALYTICS", "bob-analytics"),
		Artifacts:             or("BOB_STORAGE_BUCKETS_ARTIFACTS", "bob-artifacts"),
		WorkerProbeCredential: getenv("BOB_STORAGE_WORKER_PROBE_CREDENTIAL"),
	}
	if c.Endpoint == "" {
		return c, errors.New("storage.endpoint is not set (BOB_STORAGE_ENDPOINT)")
	}
	if c.WorkerProbeCredential != "" {
		if ak, sk, ok := strings.Cut(c.WorkerProbeCredential, ":"); !ok || ak == "" || sk == "" {
			return c, errors.New("storage.worker_probe_credential must be <access key>:<secret key>")
		}
	}
	return c, nil
}

func (c StorageConfig) bucket(name, role string) BucketConfig {
	return BucketConfig{Endpoint: c.Endpoint, Region: c.Region, Name: name, Role: role}
}

// Storage is the opened dynamitedb buckets of one bob-server process.
type Storage struct {
	Config  StorageConfig
	Control *Bucket // nil in a worker: workers have no access to the control bucket
	Fleet   *Bucket
}

// OpenStorage runs the start-up checks of R-STORE-102 and R-SEC-102 for a process of the given
// mode ("orchestrator" or "worker") and opens its dynamitedb buckets. It writes nothing but the
// probe objects, so a refused process leaves no state behind.
//
// Every bucket of the mode is probed (Open). A bucket the credential may not probe is skipped;
// at least one bucket must be probed, because all four share the endpoint. An orchestrator with
// storage.worker_probe_credential set then runs the policy self-check. A failed check returns
// a *Refusal (exit ExitConfig).
func OpenStorage(ctx context.Context, cfg StorageConfig, mode string) (*Storage, error) {
	var names []BucketConfig
	switch mode {
	case "orchestrator":
		names = []BucketConfig{cfg.bucket(cfg.Control, "control"), cfg.bucket(cfg.Fleet, "fleet"),
			cfg.bucket(cfg.Analytics, "analytics"), cfg.bucket(cfg.Artifacts, "artifacts")}
	case "worker":
		names = []BucketConfig{cfg.bucket(cfg.Fleet, "fleet"), cfg.bucket(cfg.Analytics, "analytics"),
			cfg.bucket(cfg.Artifacts, "artifacts")}
	default:
		return nil, fmt.Errorf("store: unknown mode %q", mode)
	}
	s := &Storage{Config: cfg}
	probed := 0
	for _, bc := range names {
		b, err := newBucket(ctx, bc)
		if err != nil {
			return nil, err
		}
		switch err := probe(ctx, b); {
		case err == nil:
			probed++
		case !errors.Is(err, errProbeDenied):
			return nil, err
		}
		switch bc.Role {
		case "control":
			s.Control = b
		case "fleet":
			s.Fleet = b
		}
	}
	if probed == 0 {
		return nil, fmt.Errorf("store: no bucket at %s accepts the conformance probe under _bob/probe/ with this credential", cfg.Endpoint)
	}
	if mode == "orchestrator" && cfg.WorkerProbeCredential != "" {
		if err := CheckWorkerCredential(ctx, cfg); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func readableControl() *Refusal {
	return &Refusal{
		Code: diag.Code(7008),
		Msg:  "control bucket is readable with the worker credential",
		Hint: "apply deploy/policies/control-bucket.json to the control bucket and deploy/policies/worker.json to the worker identity",
	}
}

// CheckWorkerCredential is the start-up policy self-check (R-SEC-102): the worker credential
// gets 403 on HeadObject <control>/_bob/probe, the control bucket's policy holds the deny
// statement of deploy/policies/control-bucket.json, and the worker credential gets 403 on
// PutObject <fleet>/worker/_probe. The checks run in that order and stop at the first failure,
// so a wide credential is caught before it could write fleet state.
func CheckWorkerCredential(ctx context.Context, cfg StorageConfig) error {
	ak, sk, _ := strings.Cut(cfg.WorkerProbeCredential, ":")
	w, err := s3client.New(ctx, s3client.Options{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKey: ak, SecretKey: sk})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	o, err := s3client.New(ctx, s3client.Options{Endpoint: cfg.Endpoint, Region: cfg.Region})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	_, err = w.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(cfg.Control), Key: aws.String("_bob/probe")})
	if st := s3client.Status(err); st != 403 {
		if st == 0 && err != nil {
			return fmt.Errorf("store: policy self-check: %w", failed(ctx, err))
		}
		return readableControl()
	}

	pol, err := o.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(cfg.Control)})
	if err != nil {
		if st := s3client.Status(err); st == 0 || st >= 500 {
			return fmt.Errorf("store: policy self-check: %w", failed(ctx, err))
		}
		return readableControl() // no policy on the control bucket
	}
	if pol.Policy == nil || !deniesControl(*pol.Policy, cfg.Control) {
		return readableControl()
	}

	_, err = w.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Fleet), Key: aws.String("worker/_probe"),
		Body: strings.NewReader(""), ContentLength: aws.Int64(0)})
	if st := s3client.Status(err); st != 403 {
		if st == 0 && err != nil {
			return fmt.Errorf("store: policy self-check: %w", failed(ctx, err))
		}
		if err == nil {
			_, _ = o.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cfg.Fleet), Key: aws.String("worker/_probe")})
		}
		return &Refusal{
			Code: diag.Code(7008),
			Msg:  "fleet bucket prefix worker/ is writable with the worker credential",
			Hint: "apply deploy/policies/worker.json to the worker identity",
		}
	}
	return nil
}

// deniesControl reports whether a bucket policy holds a Deny of s3:* on the control bucket and
// its objects for everybody but listed principals (deploy/policies/control-bucket.json).
func deniesControl(policy, bucket string) bool {
	var doc struct {
		Statement []struct {
			Effect    string
			Action    stringList
			Resource  stringList
			Condition map[string]map[string]stringList
		}
	}
	if json.Unmarshal([]byte(policy), &doc) != nil {
		return false
	}
	for _, s := range doc.Statement {
		if s.Effect == "Deny" && slices.Contains(s.Action, "s3:*") &&
			slices.Contains(s.Resource, "arn:aws:s3:::"+bucket) && slices.Contains(s.Resource, "arn:aws:s3:::"+bucket+"/*") &&
			len(s.Condition) > 0 {
			return true
		}
	}
	return false
}

// stringList is an IAM field that is either one string or a list of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}
