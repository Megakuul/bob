package notify

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	notifyv1 "github.com/megakuul/bob/internal/gen/bob/notify/v1"
	"github.com/megakuul/bob/internal/netguard"
	"github.com/megakuul/bob/tests/store/env"
)

// acceptCounter is a TCP listener on loopback that counts the connections it gets.
func acceptCounter(t *testing.T) (addr string, accepted *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	accepted = &atomic.Int64{}
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
	return l.Addr().String(), accepted
}

// TestDeliveryRefusesInternalReceivers (I-R2 F8): a notifier as New builds it never connects
// to a receiver on loopback (or any internal address); with the address allowed it does.
func TestDeliveryRefusesInternalReceivers(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	if netguard.LoopbackAllowed() {
		t.Skip("bobtest builds allow loopback")
	}
	be := env.Start(t)
	control := be.Open(t, "control")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	addr, accepted := acceptCounter(t)
	n := New(control, nil, nil)
	svc := Service{N: n}
	created, err := svc.CreateNotifyWebhook(ctx, connect.NewRequest(&notifyv1.CreateNotifyWebhookRequest{Webhook: &notifyv1.NotifyWebhook{
		Url: "https://" + addr + "/hook", Events: []string{BuildFinished}, Enabled: true}}))
	if err != nil {
		t.Fatal(err)
	}
	build, _ := structpb.NewStruct(map[string]any{"id": "01build"})
	if err := n.BuildFinished(ctx, "acme", build); err != nil {
		t.Fatal(err)
	}
	if err := n.Pass(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	wh, err := svc.GetNotifyWebhook(ctx, connect.NewRequest(&notifyv1.GetNotifyWebhookRequest{Id: created.Msg.GetWebhook().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	if res := wh.Msg.GetWebhook().GetLastResult(); accepted.Load() != 0 || !strings.Contains(res, "refusing to connect") {
		t.Fatalf("delivery to a loopback receiver: %d connections, last_result %q; want none and a refusal", accepted.Load(), res)
	}
	host, _, _ := net.SplitHostPort(addr)
	allow, err := netguard.New([]string{host})
	if err != nil {
		t.Fatal(err)
	}
	n.Outbound(allow)
	if err := n.Pass(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if accepted.Load() == 0 {
		t.Errorf("with the receiver allowed the delivery never connected")
	}
}
