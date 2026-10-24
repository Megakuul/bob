package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/megakuul/bob/tests/serverenv/s3sig"
	"github.com/megakuul/bob/tests/serverenv/stack"
)

// api measures, on one all-in-one dev stack of tools/devstack (orchestrator + worker + RustFS,
// seeded with projects p1/p2), the API unary p99 (B-070, R-NFR-150 table "API unary p99 (Get*,
// warm caches, RustFS local) ≤ 50 ms; List* ≤ 150 ms": ProjectService/GetProject p1, 300 calls
// after 50 warm-up calls, and ListProjects the same way; its limit is checked here, the budgets
// file carries the Get* value) and the narinfo latency (B-068, see narinfo).
func (b *bench) api() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := filepath.Join(b.work, "devstack")
	_ = os.RemoveAll(dir)
	s, err := stack.Start(ctx, stack.Options{Dir: dir, Orchestrators: 1, Workers: 1})
	if err != nil {
		return fmt.Errorf("devstack: %w", err)
	}
	defer func() { _ = s.Stop() }()
	if err := s.Seed(ctx); err != nil {
		return fmt.Errorf("devstack seed: %w", err)
	}
	c := &http.Client{Timeout: 30 * time.Second}
	call := func(method string, req any) (time.Duration, error) {
		body, err := json.Marshal(req)
		if err != nil {
			return 0, err
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Info.APIURL+"/api/"+method, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("Connect-Protocol-Version", "1")
		hr.Header.Set("Authorization", "Bearer "+s.Info.AdminToken)
		t0 := time.Now()
		resp, err := c.Do(hr)
		if err != nil {
			return 0, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		d := time.Since(t0)
		resp.Body.Close()
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, tail(string(raw), 500))
		}
		return d, nil
	}
	measure := func(method string, req any) ([]float64, error) {
		for range 50 {
			if _, err := call(method, req); err != nil {
				return nil, err
			}
		}
		var ms []float64
		for range 300 {
			d, err := call(method, req)
			if err != nil {
				return nil, err
			}
			ms = append(ms, float64(d.Microseconds())/1000)
		}
		return ms, nil
	}
	get, err := measure("bob.project.v1.ProjectService/GetProject", map[string]any{"id": "p1"})
	if err != nil {
		return err
	}
	list, err := measure("bob.project.v1.ProjectService/ListProjects", map[string]any{})
	if err != nil {
		return err
	}
	if err := b.narinfo(ctx, s, c); err != nil {
		return err
	}
	gp99, lp99 := pct(get, 0.99), pct(list, 0.99)
	b.note("GetProject n=300: median %.2f ms, p99 %.2f ms; ListProjects n=300: median %.2f ms, p99 %.2f ms (List* budget 150 ms)",
		median(get), gp99, median(list), lp99)
	b.budget("B-070", gp99, "ms", "p99", nil)
	if lp99 > 150*1.15 {
		return fmt.Errorf("ListProjects p99 %.2f ms > 150 ms (+15%%) (B-070 List* clause)", lp99)
	}
	return nil
}

// narinfo measures the narinfo latency with a warm visibility cache (B-068, R-NFR-150 table
// "p50 ≤ 5 ms, p99 ≤ 20 ms"): one public path is published the way promotion leaves it in the
// artifacts bucket (its visibility document nix/vis/<hash>.json naming a public release, and
// nix/narinfo/<hash>.narinfo), then GET /nix/<hash>.narinfo anonymously, 50 warm-up requests
// (the visibility cache keeps a document 30 s) and 300 measured.
func (b *bench) narinfo(ctx context.Context, s *stack.Stack, c *http.Client) error {
	const alphabet = "0123456789abcdfghijklmnpqrsvwxyz" // Nix base32
	h := make([]byte, 32)
	for i := range h {
		h[i] = alphabet[(i*7+3)%len(alphabet)]
	}
	hash := string(h)
	info := "StorePath: /nix/store/" + hash + "-budgetbench\nURL: nar/" + hash + ".nar.zst\nCompression: zstd\n" +
		"NarHash: sha256:0000000000000000000000000000000000000000000000000000\nNarSize: 8\nReferences: \n"
	for key, body := range map[string]string{
		"nix/vis/" + hash + ".json":        `{"v":1,"projects":["p1"],"public":["budgetbench"]}`,
		"nix/narinfo/" + hash + ".narinfo": info,
	} {
		if err := putObject(ctx, s, key, []byte(body)); err != nil {
			return err
		}
	}
	get := func() (time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Info.APIURL+"/nix/"+hash+".narinfo", nil)
		if err != nil {
			return 0, err
		}
		t0 := time.Now()
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		d := time.Since(t0)
		resp.Body.Close()
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("GET /nix/%s.narinfo: HTTP %d", hash, resp.StatusCode)
		}
		return d, nil
	}
	for range 50 {
		if _, err := get(); err != nil {
			return err
		}
	}
	var ms []float64
	for range 300 {
		d, err := get()
		if err != nil {
			return err
		}
		ms = append(ms, float64(d.Microseconds())/1000)
	}
	p50, p99 := median(ms), pct(ms, 0.99)
	b.note("narinfo (warm visibility cache) n=300: p50 %.2f ms, p99 %.2f ms (budgets 5 / 20 ms)", p50, p99)
	b.budget("B-068", p50, "ms", "median", nil)
	if p99 > 20*1.15 {
		return fmt.Errorf("narinfo p99 %.2f ms > 20 ms (+15%%) (B-068 p99 clause)", p99)
	}
	return nil
}

// putObject writes key into the artifacts bucket with the stack's root credentials (as the
// stack's seed does).
func putObject(ctx context.Context, s *stack.Stack, key string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.Info.S3URL+"/bob-artifacts/"+key, bytes.NewReader(body))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	req.ContentLength = int64(len(body))
	s3sig.Sign(req, s3sig.Creds{AccessKey: s.Info.AccessKey, SecretKey: s.Info.SecretKey, Region: s.Info.Region},
		hex.EncodeToString(sum[:]), time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("put %s: HTTP %d %s", key, resp.StatusCode, b)
	}
	return nil
}

// pct is the p-quantile (nearest rank) of s.
func pct(s []float64, p float64) float64 {
	c := slices.Clone(s)
	slices.Sort(c)
	if len(c) == 0 {
		return 0
	}
	return c[min(len(c)-1, int(p*float64(len(c))))]
}
