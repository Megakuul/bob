// Package workerclient is a worker's Attach client for the server suites (Gate H ruling G20):
// it registers a fresh worker identity through the fleet bucket like a real worker (signed
// registration, approval by an orchestrator's registrar, sealed bwt1 token), then opens
// WorkerControlService/Attach with the token and a proof of possession. A test then speaks the
// worker side of the protocol itself, including messages a hostile worker would send (forged
// LiveChunks, R-LOG-102; forged results, R-PIPE-003).
package workerclient

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
)

// attachPath is the path the orchestrator verifies the proof of possession against (the API
// is mounted under /api/, SPEC §6.2).
const attachPath = "/api" + workerv1connect.WorkerControlServiceAttachProcedure

// Client is one registered worker identity.
type Client struct {
	ID    *worker.Identity
	Fleet *store.Bucket
	Token *worker.Token
}

// Register creates a fresh identity with the given profile, registers it in the fleet bucket
// (opened with the worker S3 credential) and waits until an orchestrator approves it and its
// sealed token opens. It fails when ctx ends first.
func Register(ctx context.Context, fleet *store.Bucket, profile store.WorkerProfile) (*Client, error) {
	id, err := worker.NewIdentity(rand.Reader)
	if err != nil {
		return nil, err
	}
	id.Profile = profile
	if _, err := worker.Register(ctx, fleet, id, time.Now()); err != nil {
		return nil, fmt.Errorf("workerclient: register: %w", err)
	}
	for {
		s, err := worker.FetchSealed(ctx, fleet, id.ID())
		switch {
		case err == nil:
			tok, err := id.OpenToken(s)
			if err != nil {
				return nil, fmt.Errorf("workerclient: %w", err)
			}
			return &Client{ID: id, Fleet: fleet, Token: tok}, nil
		case !errors.Is(err, store.ErrNotFound):
			return nil, fmt.Errorf("workerclient: worker record: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("workerclient: worker %s not approved: %w", id.ID(), ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// httpClient speaks HTTP/2 to base: cleartext HTTP/2 (h2c) for http://, TLS without
// certificate verification for https:// (loopback test servers with throwaway keys; a real
// worker pins the SPKI hash of fleet/cfg.m instead).
func httpClient(base string) *http.Client {
	tr := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}
	p := new(http.Protocols)
	if strings.HasPrefix(base, "https://") {
		p.SetHTTP2(true)
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // loopback test server
	} else {
		p.SetUnencryptedHTTP2(true)
	}
	tr.Protocols = p
	return &http.Client{Transport: tr}
}

// Stream is one open Attach stream.
type Stream struct {
	bs     *connect.BidiStreamForClient[workerv1.AttachRequest, workerv1.AttachResponse]
	cancel context.CancelFunc
	sendMu sync.Mutex

	// In receives every orchestrator message; it is closed when the stream ends.
	In <-chan *workerv1.AttachResponse

	mu  sync.Mutex
	err error
}

// Attach opens WorkerControlService/Attach on the orchestrator at base (its API listener or
// its attach front, without the /api suffix), sends hello (its worker id is filled in) and
// waits for the Welcome.
func (c *Client) Attach(ctx context.Context, base string, hello *workerv1.Hello) (*Stream, error) {
	base = strings.TrimRight(base, "/")
	cl := workerv1connect.NewWorkerControlServiceClient(httpClient(base), base+"/api")
	sctx, cancel := context.WithCancel(ctx)
	bs := cl.Attach(sctx)
	bs.RequestHeader().Set("Authorization", c.Token.Authorization())
	bs.RequestHeader().Set("Bob-PoP", c.Token.PoP(c.ID, http.MethodPost, attachPath, time.Now()))
	if hello == nil {
		hello = &workerv1.Hello{}
	}
	hello.WorkerId = c.ID.ID()
	if err := bs.Send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Hello{Hello: hello}}); err != nil {
		cancel()
		return nil, fmt.Errorf("workerclient: send hello: %w", err)
	}
	first, err := bs.Receive()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("workerclient: attach: %w", err)
	}
	if first.GetWelcome() == nil {
		cancel()
		return nil, fmt.Errorf("workerclient: attach: first message is %T, want Welcome", first.GetMsg())
	}
	in := make(chan *workerv1.AttachResponse, 1024)
	s := &Stream{bs: bs, cancel: cancel, In: in}
	go func() {
		defer close(in)
		for {
			m, err := bs.Receive()
			if err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
				return
			}
			select {
			case in <- m:
			default: // a test that does not read In must not stall the stream
			}
		}
	}()
	return s, nil
}

// Send sends one message (safe for concurrent use).
func (s *Stream) Send(m *workerv1.AttachRequest) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.bs.Send(m)
}

// SendChunk sends one LiveChunk.
func (s *Stream) SendChunk(c *workerv1.LiveChunk) error {
	return s.Send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_LiveChunk{LiveChunk: c}})
}

// Err is the error that ended the receive side ("" while open).
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the stream.
func (s *Stream) Close() error {
	s.sendMu.Lock()
	err := s.bs.CloseRequest()
	s.sendMu.Unlock()
	s.cancel()
	return err
}
