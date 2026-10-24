//go:build live

package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/forge"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	"github.com/megakuul/bob/internal/netguard"
	"github.com/megakuul/bob/tests/serverenv/forgejo"
	"github.com/megakuul/bob/tests/store/env"
)

// TestLiveForgejoCheckoutPath runs the orchestrator side of §6.9.6 against a live Forgejo and
// RustFS: integration → link → MintForJob (a repo-scoped job token that fetches) → Revoke →
// name sweep, and a commit status through the link.
func TestLiveForgejoCheckoutPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fj, err := forgejo.Start(ctx, forgejo.Options{Dir: t.TempDir(), Seed: true})
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	t.Cleanup(func() { _ = fj.Stop() })
	// The adapters use the system roots, read once: trust the instance before the first TLS use.
	t.Setenv("SSL_CERT_FILE", fj.CA)
	if err := fj.API(ctx, http.MethodPut, "/api/v1/repos/alice/demo/collaborators/bobbot", forgejo.Auth{Token: fj.Seeded.OwnerToken},
		map[string]any{"permission": "admin"}, nil); err != nil {
		t.Fatal(err)
	}
	be := env.Start(t)
	// the instance runs on loopback, which the outgoing-address policy refuses unless allowed
	loopback, err := netguard.New([]string{"127.0.0.0/8", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	reg := &Registry{Control: be.Open(t, "control"), Outbound: loopback}
	svc := &Service{Registry: reg, BaseURL: "https://127.0.0.1:1"}
	if _, err := svc.CreateForgeIntegration(ctx, connect.NewRequest(&forgev1.CreateForgeIntegrationRequest{Integration: &forgev1.ForgeIntegration{
		Id: "forgejo-live", Kind: forgev1.ForgeKind_FORGE_KIND_FORGEJO, BaseUrl: fj.URL,
		Credentials: &forgev1.ForgeIntegration_Forgejo{Forgejo: &forgev1.ForgejoCredentials{BotLogin: "bobbot", BotPassword: forgejo.UserPass("bobbot")}}}})); err != nil {
		t.Fatal(err)
	}
	linked, err := reg.LinkRepo(ctx, "forgejo-live", "alice/demo", "acme", "https://127.0.0.1:1")
	if err != nil || linked.HookErr != nil {
		t.Fatalf("LinkRepo = %+v, %v", linked, err)
	}
	if _, err := reg.LinkRepo(ctx, "forgejo-live", "alice/demo", "other", "https://127.0.0.1:1"); !errors.Is(err, ErrLinked) {
		t.Errorf("second link to another project: %v, want ErrLinked", err)
	}
	m := &Minter{Registry: reg}
	repo := "forgejo-live/" + strconv.FormatInt(linked.Repo.ID, 10)
	handle, co, err := m.MintForJob(ctx, "01JOBLIVE", repo, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		dir := t.TempDir()
		run := func(args ...string) error {
			cmd := exec.Command("git", args...)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_SSL_CAINFO="+fj.CA, "GIT_TERMINAL_PROMPT=0", "HOME="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return errors.New(strings.TrimSpace(string(out)))
			}
			return nil
		}
		if err := run("init", "-q"); err != nil {
			return err
		}
		auth := base64.StdEncoding.EncodeToString([]byte(co.Username + ":" + co.Token))
		return run("-c", "http.extraHeader=Authorization: Basic "+auth, "fetch", "--depth=1", co.CloneURL, "main")
	}
	if err := fetch(); err != nil {
		t.Fatalf("fetch with the minted token: %v", err)
	}
	if err := reg.SetStatus(ctx, "forgejo-live", linked.Repo.ID, forge.Status{SHA: strings.Repeat("0", 39) + "1", Context: "bob/x", State: forge.StatePending}); err == nil {
		t.Logf("status on an unknown commit accepted by Forgejo")
	}
	for i := 0; i < 2; i++ {
		if err := m.Revoke(ctx, handle); err != nil {
			t.Fatalf("Revoke #%d: %v", i+1, err)
		}
	}
	if err := fetch(); err == nil {
		t.Errorf("fetch with a revoked token succeeded")
	}
	if err := m.SweepNames(ctx); err != nil {
		t.Errorf("SweepNames: %v", err)
	}
	rec, err := reg.Record(ctx, "forgejo-live")
	if err != nil || rec.Health != int32(forgev1.Health_HEALTH_OK) {
		t.Errorf("integration health after successful calls = %v (%v)", rec.Health, err)
	}
}
