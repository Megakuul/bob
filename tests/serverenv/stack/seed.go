package stack

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

// Seed is the fixed data set of a seeded stack (the harness contract of tests/auth/authenv
// Seed): projects p1 and p2 and one build of each. The builds are created from an uploaded
// source snapshot and cancelled as soon as they have a job, so they exist (with a job) without
// occupying the workers.
type Seed struct {
	Projects []string          `json:"projects"`
	Builds   map[string]string `json:"builds"` // project → build id
}

// SeedProjects are the projects of the seed.
var SeedProjects = []string{"p1", "p2"}

// Seed creates the seed through the public API of the first orchestrator (as the bootstrap
// admin) and the artifacts bucket (the snapshot upload, as the harness root), and records it
// in Info.Seed and devstack.json.
func (s *Stack) Seed(ctx context.Context) error {
	if s.Info.APIURL == "" || s.Info.AdminToken == "" {
		return errors.New("devstack seed: no orchestrator API")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	c := &apiClient{base: s.Info.APIURL, token: s.Info.AdminToken, http: &http.Client{Timeout: 30 * time.Second}}
	for {
		if err := c.call(ctx, "bob.auth.v1.AuthService/WhoAmI", map[string]any{}, nil); err == nil {
			break
		} else if ctx.Err() != nil {
			return fmt.Errorf("devstack seed: the orchestrator API did not answer: %w", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	snap, sha, err := emptySnapshot()
	if err != nil {
		return err
	}
	seed := &Seed{Builds: map[string]string{}}
	for _, p := range SeedProjects {
		err := c.call(ctx, "bob.project.v1.ProjectService/CreateProject", map[string]any{
			"project": map[string]any{"id": p, "displayName": p, "description": "devstack seed"}}, nil)
		if err != nil && !isCode(err, "already_exists") {
			return fmt.Errorf("devstack seed: CreateProject %s: %w", p, err)
		}
		if err := s.putArtifact(ctx, "src/"+p+"/"+sha+".tar.zst", snap); err != nil {
			return err
		}
		var created struct {
			Build struct {
				ID string `json:"id"`
			} `json:"build"`
		}
		if err := c.call(ctx, "bob.build.v1.BuildService/CreateBuild", map[string]any{
			"projectId": p, "source": map[string]any{"uploadSha256": sha}, "targets": []string{"//seed:all"},
			"reason": "devstack seed"}, &created); err != nil {
			return fmt.Errorf("devstack seed: CreateBuild %s: %w", p, err)
		}
		// let the controller plan the build so it has a job (the RBAC matrix resolves job ids
		// of the seed), then cancel it before a worker spends time on it
		for wait := time.Now().Add(15 * time.Second); time.Now().Before(wait); {
			var jobs struct {
				Jobs []json.RawMessage `json:"jobs"`
			}
			if err := c.call(ctx, "bob.build.v1.JobService/ListJobs", map[string]any{"buildId": created.Build.ID}, &jobs); err == nil && len(jobs.Jobs) > 0 {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err := c.call(ctx, "bob.build.v1.BuildService/CancelBuild", map[string]any{"id": created.Build.ID}, nil); err != nil &&
			!isCode(err, "failed_precondition") {
			return fmt.Errorf("devstack seed: CancelBuild %s: %w", created.Build.ID, err)
		}
		seed.Projects = append(seed.Projects, p)
		seed.Builds[p] = created.Build.ID
	}
	s.Info.Seed = seed
	return s.writeInfo()
}

// emptySnapshot is a source snapshot (zstd tar) holding one README, and its sha256.
func emptySnapshot() ([]byte, string, error) {
	var t bytes.Buffer
	tw := tar.NewWriter(&t)
	body := []byte("devstack seed\n")
	if err := tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, "", err
	}
	if _, err := tw.Write(body); err != nil {
		return nil, "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	var z bytes.Buffer
	zw, err := zstd.NewWriter(&z)
	if err != nil {
		return nil, "", err
	}
	if _, err := zw.Write(t.Bytes()); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(z.Bytes())
	return z.Bytes(), hex.EncodeToString(sum[:]), nil
}

// putArtifact writes an object into the artifacts bucket with the harness root key.
func (s *Stack) putArtifact(ctx context.Context, key string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.Info.S3URL+"/bob-artifacts/"+key, bytes.NewReader(body))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	req.ContentLength = int64(len(body))
	s3sig.Sign(req, s3sig.Creds{AccessKey: s.Info.AccessKey, SecretKey: s.Info.SecretKey, Region: s.Info.Region}, hex.EncodeToString(sum[:]), time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("devstack seed: put %s: %w", key, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("devstack seed: put %s: HTTP %d %s", key, resp.StatusCode, b)
	}
	return nil
}

// apiClient calls the Connect JSON API with a bearer token.
type apiClient struct {
	base, token string
	http        *http.Client
}

// apiError is a Connect error answer.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	status  int
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d %s: %s", e.status, e.Code, e.Message) }

func isCode(err error, code string) bool {
	var e *apiError
	return errors.As(err, &e) && e.Code == code
}

func (c *apiClient) call(ctx context.Context, method string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Connect-Protocol-Version", "1")
	hr.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		e := &apiError{status: resp.StatusCode}
		_ = json.Unmarshal(raw, e)
		return e
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
