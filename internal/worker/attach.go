package worker

import (
	"context"
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
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
)

// attachPath is the path the Attach proof of possession signs.
const attachPath = "/api/" + workerv1connect.WorkerControlServiceAttachProcedure

// Attach timing: slot states every slotEvery; a stream is reopened attachRenew before its
// token expires (new token, new PoP); a failed stream is retried with 1–30 s backoff.
const (
	slotEvery   = 5 * time.Second
	attachRenew = 5 * time.Minute
)

// token returns the current worker token.
func (p *Process) token() *Token {
	p.tokMu.Lock()
	defer p.tokMu.Unlock()
	return p.tok
}

// refreshToken re-reads worker/<wid> every 30 s and opens the token the registrar rotated in
// (at half life, R-FLEET-002 token lifecycle).
func (p *Process) refreshToken(ctx context.Context) error {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		s, err := FetchSealed(ctx, p.cfg.Fleet, p.id.ID())
		if err != nil {
			continue // revocation is the job loop's (worker record refresh)
		}
		if cur := p.token(); cur != nil && s.Serial <= cur.Serial() {
			continue
		}
		tok, err := p.id.OpenToken(s)
		if err != nil {
			p.cfg.Log.Warn("worker: rotated token does not open", "err", err.Error())
			continue
		}
		p.tokMu.Lock()
		p.tok = tok
		p.tokMu.Unlock()
	}
}

// attachAddrs are the orchestrators to attach to: BOB_ORCHESTRATOR_ADDRS, else the members
// of fleet/cfg.m.
func (p *Process) attachAddrs(ctx context.Context) []string {
	if len(p.cfg.Orchestrators) > 0 {
		return p.cfg.Orchestrators
	}
	fc, err := store.Get(ctx, p.cfg.Fleet, store.FleetConfigKey())
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range fc.Members {
		if strings.HasPrefix(m.Endpoint, "http://") || strings.HasPrefix(m.Endpoint, "https://") {
			out = append(out, m.Endpoint)
		}
	}
	return out
}

// attachClient speaks HTTP/2 to base: h2c for http://, TLS for https:// (Attach is a
// bidirectional stream).
func attachClient(base string) *http.Client {
	tr := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	pr := new(http.Protocols)
	if strings.HasPrefix(base, "https://") {
		pr.SetHTTP2(true)
	} else {
		pr.SetUnencryptedHTTP2(true)
	}
	tr.Protocols = pr
	return &http.Client{Transport: tr}
}

// attachLoop keeps one Attach stream to the orchestrator at base open until ctx ends. Without
// it the worker still works (poll, claim, result through S3, R-SRV-100).
func (p *Process) attachLoop(ctx context.Context, base string) error {
	base = strings.TrimRight(base, "/")
	cl := workerv1connect.NewWorkerControlServiceClient(attachClient(base), base+"/api")
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := p.attachOnce(ctx, cl)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		if err != nil {
			p.cfg.Log.Debug("worker: attach stream ended", "orchestrator", base, "err", err.Error())
		}
		if !sleep(ctx, backoff) {
			return nil
		}
		backoff = min(2*backoff, 30*time.Second)
	}
	return nil
}

// attachOnce runs one Attach stream: Hello, then wake-ups and live log subscriptions in,
// slot states and live chunks out, until the token is due for renewal or the stream fails.
func (p *Process) attachOnce(ctx context.Context, cl workerv1connect.WorkerControlServiceClient) error {
	tok := p.token()
	if tok == nil {
		return errors.New("no token")
	}
	until := time.Until(tok.Expiry()) - attachRenew
	if until <= 0 {
		return errors.New("token about to expire; waiting for the rotated one")
	}
	sctx, cancel := context.WithTimeout(ctx, until)
	defer cancel()
	bs := cl.Attach(sctx)
	bs.RequestHeader().Set("Authorization", tok.Authorization())
	bs.RequestHeader().Set("Bob-PoP", tok.PoP(p.id, http.MethodPost, attachPath, p.cfg.Now()))
	var sendMu sync.Mutex
	send := func(m *workerv1.AttachRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return bs.Send(m)
	}
	defer func() {
		sendMu.Lock()
		_ = bs.CloseRequest()
		sendMu.Unlock()
		_ = bs.CloseResponse()
	}()
	w := p.cur.Load()
	hello := &workerv1.Hello{WorkerId: p.id.ID(), Platform: p.id.Profile.Platform, SlotsTotal: int32(p.cfg.Slots)}
	if w != nil {
		hello.SlotsFree, hello.ClassIds = int32(w.FreeSlots()), w.classes
	}
	if err := send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Hello{Hello: hello}}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	first, err := bs.Receive()
	if err != nil {
		return err
	}
	if first.GetWelcome() == nil {
		return fmt.Errorf("first message is %T, want Welcome", first.GetMsg())
	}
	var bg []<-chan error
	defer func() {
		cancel()
		for _, c := range bg {
			<-c
		}
	}()
	var ls *LiveStream
	if p.logs != nil {
		ls = p.logs.Stream(func(c *workerv1.LiveChunk) error {
			return send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_LiveChunk{LiveChunk: c}})
		})
		bg = append(bg, safe.Go(func() error { return ls.Run(sctx) }))
	}
	bg = append(bg, safe.Go(func() error {
		t := time.NewTicker(slotEvery)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return nil
			case <-t.C:
			}
			if w := p.cur.Load(); w != nil {
				if err := send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Slots{Slots: &workerv1.SlotState{SlotsFree: int32(w.FreeSlots())}}}); err != nil {
					cancel()
					return nil
				}
			}
		}
	}))
	for {
		m, err := bs.Receive()
		if err != nil {
			if sctx.Err() != nil || connect.CodeOf(err) == connect.CodeCanceled {
				return nil
			}
			return err
		}
		if ls != nil && ls.Handle(m) {
			continue
		}
		switch x := m.Msg.(type) {
		case *workerv1.AttachResponse_Wake:
			if w := p.cur.Load(); w != nil {
				w.Wake(x.Wake.ClassId, x.Wake.Slot, x.Wake.JobId)
			}
		case *workerv1.AttachResponse_Ping:
			_ = send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Pong{Pong: &workerv1.Pong{UnixMs: x.Ping.UnixMs}}})
		}
		// CancelJob is a hint: the job watch reads the cancel flag on job/<id> itself. Drain is
		// not acted on yet (the loop has no drain state; NEEDS in plan/progress/I-B6.md).
	}
}
