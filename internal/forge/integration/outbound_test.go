package integration

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	"github.com/megakuul/bob/internal/netguard"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRegistryRefusesInternalForges (I-R2 F8): the adapters a Registry builds never connect to
// a forge on loopback (or any internal address) unless its Outbound policy allows it.
func TestRegistryRefusesInternalForges(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	if netguard.LoopbackAllowed() {
		t.Skip("bobtest builds allow loopback")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var accepted atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	be := env.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reg := &Registry{Control: be.Open(t, "control")}
	svc := &Service{Registry: reg, BaseURL: "https://bob.example"}
	if _, err := svc.CreateForgeIntegration(ctx, connect.NewRequest(&forgev1.CreateForgeIntegrationRequest{Integration: &forgev1.ForgeIntegration{
		Id: "internal", Kind: forgev1.ForgeKind_FORGE_KIND_FORGEJO, BaseUrl: "https://" + l.Addr().String(),
		Credentials: &forgev1.ForgeIntegration_Forgejo{Forgejo: &forgev1.ForgejoCredentials{BotLogin: "bobbot", BotPassword: "pw"}}}})); err != nil {
		t.Fatal(err)
	}
	_, err = reg.LinkRepo(ctx, "internal", "alice/demo", "acme", "https://bob.example")
	if err == nil || !strings.Contains(err.Error(), "refusing to connect") || accepted.Load() != 0 {
		t.Fatalf("LinkRepo against a loopback forge: %v (%d connections); want a refusal and no connection", err, accepted.Load())
	}
	allow, err := netguard.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	reg2 := &Registry{Control: reg.Control, Outbound: allow}
	_, _ = reg2.LinkRepo(ctx, "internal", "alice/demo", "acme", "https://bob.example")
	if accepted.Load() == 0 {
		t.Errorf("with loopback allowed the adapter never connected")
	}
}
