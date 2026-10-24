package workerclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// echoControl is a WorkerControlService that welcomes the worker, sends one LiveSubscribe and
// records what the worker sends.
type echoControl struct {
	workerv1connect.UnimplementedWorkerControlServiceHandler
	mu     sync.Mutex
	wid    string
	chunks []*workerv1.LiveChunk
}

func (e *echoControl) Attach(ctx context.Context, st *connect.BidiStream[workerv1.AttachRequest, workerv1.AttachResponse]) error {
	e.mu.Lock()
	e.wid = api.WorkerFrom(ctx)
	e.mu.Unlock()
	if _, err := st.Receive(); err != nil {
		return err
	}
	if err := st.Send(&workerv1.AttachResponse{Msg: &workerv1.AttachResponse_Welcome{Welcome: &workerv1.Welcome{OrchestratorId: "o1"}}}); err != nil {
		return err
	}
	if err := st.Send(&workerv1.AttachResponse{Msg: &workerv1.AttachResponse_LiveSubscribe{LiveSubscribe: &workerv1.LiveSubscribe{SubscriptionId: "s1", JobId: "j1"}}}); err != nil {
		return err
	}
	for {
		m, err := st.Receive()
		if err != nil {
			return nil
		}
		e.mu.Lock()
		e.chunks = append(e.chunks, m.GetLiveChunk())
		e.mu.Unlock()
	}
}

// TestRegisterAndAttach: a fresh identity is approved by a real Registrar, its bwt1 token and
// PoP pass the API's worker authentication, and the Attach stream carries messages both ways
// over cleartext HTTP/2. A token presented with another identity's PoP is refused.
func TestRegisterAndAttach(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fs, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Stop() }()
	t.Setenv("AWS_ACCESS_KEY_ID", rustfs.RootAccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", rustfs.RootSecretKey)
	open := func(name, role string) *store.Bucket {
		b, err := store.Open(ctx, store.BucketConfig{Endpoint: fs.URL, Region: "us-east-1", Name: name, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	control, fleet := open("bob-control", "control"), open("bob-fleet", "fleet")
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	reg, err := orchestrator.NewRegistrar(orchestrator.RegistrarConfig{Control: control, Fleet: fleet, Name: "o1", Key: key, Kid: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	ctl := &echoControl{}
	srv, err := api.New(api.Config{Authn: &api.StaticToken{}, Workers: reg, Services: api.Services{WorkerControl: ctl}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv, Protocols: new(http.Protocols)}
	hs.Protocols.SetHTTP1(true)
	hs.Protocols.SetUnencryptedHTTP2(true)
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()
	base := "http://" + ln.Addr().String()

	passes, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		for passes.Err() == nil {
			_, _ = reg.Pass(passes)
			time.Sleep(200 * time.Millisecond)
		}
	}()
	c, err := Register(ctx, fleet, store.WorkerProfile{Platform: "aarch64-linux"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Attach(ctx, base, &workerv1.Hello{Platform: "aarch64-linux", SlotsTotal: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-s.In:
		if m.GetLiveSubscribe().GetSubscriptionId() != "s1" {
			t.Fatalf("second message %+v, want the LiveSubscribe", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no LiveSubscribe")
	}
	if err := s.SendChunk(&workerv1.LiveChunk{SubscriptionId: "s1", JobId: "j1", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctl.mu.Lock()
		n, wid := len(ctl.chunks), ctl.wid
		ctl.mu.Unlock()
		if n == 1 {
			if wid != c.ID.ID() {
				t.Fatalf("server saw worker %q, want %q", wid, c.ID.ID())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the chunk did not arrive")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = s.Close()

	// another identity's PoP with this token
	other, err := Register(ctx, fleet, store.WorkerProfile{Platform: "aarch64-linux"})
	if err != nil {
		t.Fatal(err)
	}
	thief := &Client{ID: other.ID, Fleet: fleet, Token: c.Token}
	if _, err := thief.Attach(ctx, base, nil); err == nil || !strings.Contains(err.Error(), "proof of possession") {
		t.Fatalf("token with a foreign PoP: %v, want unauthenticated: proof of possession invalid", err)
	}
}
