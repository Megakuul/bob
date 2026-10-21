// Package forgefix loads the recorded forge payloads under
// tests/fixtures/forges/ (from spike S16) for the forge-fixtures suite.
//
// Layout:
//
//	forgejo/<event>.{headers,body}.json      named Forgejo 16.0.6 deliveries (exact bytes + headers)
//	forgejo/raw/NNN-<event>.{headers,body}.json   the full captured sequence
//	forgejo/SECRET.test                      webhook secret of the recordings
//	gitlab/live/NN-<event>.{headers,body}.json    live GitLab 19.4.2 deliveries
//	gitlab/live/SIGNING_TOKEN.test           Standard Webhooks signing token
//	gitlab/{derived,sdk-testdata}/*.json     bodies only (client-go test data, derived variants)
//	github/webhooks/*.json                   bodies only (octokit examples, derived variants)
//	github/api/*.json                        documented GitHub API responses (replay server)
package forgefix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/tests/serverenv"
)

// Delivery is one recorded webhook delivery.
type Delivery struct {
	Name    string      // file stem, e.g. "push.main" or "01-push_hook"
	Headers http.Header // nil for body-only fixtures
	Body    []byte      // exact bytes as delivered
}

// Dir returns tests/fixtures/forges/<sub>.
func Dir(sub ...string) (string, error) {
	root, err := serverenv.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root, "tests", "fixtures", "forges"}, sub...)...), nil
}

// Load reads every delivery in tests/fixtures/forges/<sub> (e.g. "forgejo",
// "forgejo/raw", "gitlab/live", "github/webhooks"), sorted by name. A
// <stem>.body.json with a <stem>.headers.json is one delivery with headers;
// any other *.json is a body-only delivery.
func Load(sub string) ([]Delivery, error) {
	dir, err := Dir(sub)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("forgefix: %w", err)
	}
	var out []Delivery
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".headers.json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("forgefix: %w", err)
		}
		stem, paired := strings.CutSuffix(n, ".body.json")
		if !paired {
			stem = strings.TrimSuffix(n, ".json")
		}
		d := Delivery{Name: stem, Body: body}
		if paired {
			hb, err := os.ReadFile(filepath.Join(dir, stem+".headers.json"))
			if err != nil {
				return nil, fmt.Errorf("forgefix: delivery %s/%s has no headers: %w", sub, stem, err)
			}
			var h map[string]string
			if err := json.Unmarshal(hb, &h); err != nil {
				return nil, fmt.Errorf("forgefix: %s/%s.headers.json: %w", sub, stem, err)
			}
			d.Headers = http.Header{}
			for k, v := range h {
				d.Headers.Set(k, v)
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Secret reads a secret file such as "forgejo/SECRET.test" or
// "gitlab/live/SIGNING_TOKEN.test", trimmed.
func Secret(path string) (string, error) {
	p, err := Dir(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("forgefix: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Request builds the HTTP request that replays d against url.
func (d Delivery) Request(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(d.Body)))
	if err != nil {
		return nil, fmt.Errorf("forgefix: %w", err)
	}
	for k, v := range d.Headers {
		if k == "Content-Length" || k == "Connection" || k == "Accept-Encoding" {
			continue // transport headers; the client sets them
		}
		req.Header[k] = v
	}
	return req, nil
}
