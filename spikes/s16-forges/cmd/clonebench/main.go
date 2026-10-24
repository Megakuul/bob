// clonebench mints a Forgejo checkout token via the adapter, clones a repository like a worker
// would (credential only in a one-shot http.extraHeader), revokes, and prints timings as JSON.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bobspike/s16/forge"
	"bobspike/s16/forge/forgejo"
)

func main() {
	base := flag.String("url", "https://127.0.0.1:21600", "forgejo URL")
	repo := flag.String("repo", "alice/cobra", "owner/name")
	repoID := flag.Int64("repo-id", 0, "repository id")
	runs := flag.Int("runs", 5, "runs per mode")
	work := flag.String("work", "/root/scratch-s16/worker", "worker dir")
	ref := flag.String("ref", "main", "branch for shallow modes")
	flag.Parse()
	if err := run(*base, *repo, *repoID, *runs, *work, *ref); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(base, repo string, repoID int64, runs int, work, ref string) error {
	pem, err := os.ReadFile(os.Getenv("S16_CA"))
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	a, err := forgejo.New(forgejo.Config{BaseURL: base, BotUser: "bobbot", BotPassword: forge.NewSecret(os.Getenv("S16_BOT_PASS")), HTTPClient: hc, ServerVersion: "16.0.6"})
	if err != nil {
		return err
	}
	r := forge.Repo{ID: repoID, FullName: repo, CloneURL: base + "/" + repo + ".git"}
	modes := map[string][]string{
		"full":         {"clone", "-q"},
		"depth1":       {"clone", "-q", "--depth=1", "--single-branch", "--branch", ref},
		"blobless":     {"clone", "-q", "--filter=blob:none", "--single-branch", "--branch", ref},
		"depth1-nockt": {"clone", "-q", "--depth=1", "--single-branch", "--branch", ref, "--no-checkout"},
	}
	res := map[string]any{}
	var mint, revoke []time.Duration
	for mode, args := range modes {
		var ds []time.Duration
		for i := 0; i < runs; i++ {
			ctx := context.Background()
			t0 := time.Now()
			co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: r, JobID: fmt.Sprintf("bench-%s-%d-%d", mode, i, time.Now().UnixNano())})
			if err != nil {
				return err
			}
			mint = append(mint, time.Since(t0))
			dir := filepath.Join(work, fmt.Sprintf("bench-%s-%d", mode, i))
			os.RemoveAll(dir)
			auth := base64.StdEncoding.EncodeToString([]byte(co.Username + ":" + co.Token.Reveal()))
			cmdArgs := append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth}, args...)
			cmdArgs = append(cmdArgs, co.CloneURL, dir)
			cmd := exec.Command("git", cmdArgs...)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_CA"))
			t1 := time.Now()
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s: %v %s", mode, err, out)
			}
			ds = append(ds, time.Since(t1))
			t2 := time.Now()
			if err := a.RevokeCheckout(ctx, co); err != nil {
				return err
			}
			revoke = append(revoke, time.Since(t2))
			if i == 0 {
				du, _ := exec.Command("du", "-sk", dir).Output()
				res[mode+"_kib"] = strings.Fields(string(du))[0]
			}
			os.RemoveAll(dir)
		}
		res[mode+"_ms"] = stats(ds)
	}
	res["mint_ms"] = stats(mint)
	res["revoke_ms"] = stats(revoke)
	res["repo"] = repo
	return json.NewEncoder(os.Stdout).Encode(res)
}

func stats(ds []time.Duration) map[string]float64 {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return map[string]float64{"min": ms(ds[0]), "median": ms(ds[len(ds)/2]), "max": ms(ds[len(ds)-1])}
}
