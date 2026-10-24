package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/fleet"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
)

// attachPath is the path the Attach proof of possession signs.
const attachPath = "/api" + workerv1connect.WorkerControlServiceAttachProcedure // "/api/bob.worker.v1…/Attach"

// Attach timing: slot states every slotEvery and when the free count changes (checked every
// slotPoll); a stream is reopened attachRenew before its
// token expires (new token, new PoP); a failed stream is retried with 1–30 s backoff.
const (
	slotEvery   = 5 * time.Second
	slotPoll    = 250 * time.Millisecond
	attachRenew = 5 * time.Minute
)

// Dead-connection detection (A291). A connection whose peer vanished without a FIN or RST (a
// killed host, a cut path, a proxy that lost its backend) otherwise looks open until TCP gives
// up after many minutes, and the worker never reconnects. HTTP/2 health checks: after
// attachPingIdle without a received frame the transport sends a PING and closes the connection
// when no answer comes within attachPingTimeout. Above that, a stream that receives no message
// for attachSilence (the orchestrator pings every 30 s) is ended by the worker and reopened.
const (
	attachPingIdle    = 10 * time.Second
	attachPingTimeout = 5 * time.Second
	attachSilence     = 75 * time.Second
)

// errAttachSilent ends a stream on which nothing arrived for attachSilence.
var errAttachSilent = errors.New("attach stream silent")

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
	for _, m := range fc.Members.Value() {
		if strings.HasPrefix(m.Endpoint, "http://") || strings.HasPrefix(m.Endpoint, "https://") {
			out = append(out, m.Endpoint)
		}
	}
	return out
}

// attachPin is what a worker expects of the server at one Attach address (R-SRV-102). For a
// member endpoint read from fleet/cfg the server must prove that member's pin. For a configured
// address (BOB_ORCHESTRATOR_ADDRS) the server must prove the pin of the member it names in
// Welcome, and the address stays bound to the first member that did: a redirected address
// that now leads to another orchestrator is refused.
type attachPin struct {
	base   string
	static bool
	bound  string // orchestrator id this address proved first (static addresses)
}

// checkPin verifies the Attach proof of the server at pin.base against fleet/cfg.m.
func (p *Process) checkPin(ctx context.Context, pin *attachPin, orch, proof, nonce string) error {
	fc, err := store.Get(ctx, p.cfg.Fleet, store.FleetConfigKey())
	if err != nil {
		return fmt.Errorf("attach pin: fleet/cfg: %w", err)
	}
	var want *store.FleetMember
	for _, m := range fc.Members.Value() {
		if pin.static && m.Orch == orch || !pin.static && strings.TrimRight(m.Endpoint, "/") == pin.base {
			want = &m
			break
		}
	}
	switch {
	case want == nil:
		return fmt.Errorf("attach pin: %w: %q is not a member of fleet/cfg", fleet.ErrPin, orch)
	case pin.static && pin.bound != "" && pin.bound != orch:
		return fmt.Errorf("attach pin: %w: this address belongs to %s, not %s", fleet.ErrPin, pin.bound, orch)
	}
	if err := fleet.VerifyAttachProof(proof, nonce, want.SPKIPin); err != nil {
		return fmt.Errorf("attach pin of %s: %w", want.Orch, err)
	}
	if pin.static {
		pin.bound = orch
	}
	return nil
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
	tr.HTTP2 = &http.HTTP2Config{SendPingTimeout: attachPingIdle, PingTimeout: attachPingTimeout}
	return &http.Client{Transport: tr}
}

// connSet records the connections a client dialed.
type connSet struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (c *connSet) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.conns {
		_ = n.Close()
	}
	c.conns = nil
}

// attachStreamClient is attachClient whose connections closeAll ends.
func attachStreamClient(base string) (*http.Client, *connSet) {
	hc := attachClient(base)
	cs := &connSet{}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		return hc, cs
	}
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err == nil {
			cs.mu.Lock()
			cs.conns = append(cs.conns, c)
			cs.mu.Unlock()
		}
		return c, err
	}
	return hc, cs
}

// attachLoop keeps one Attach stream to the orchestrator at base open until ctx ends. Without
// it the worker still works (poll, claim, result through S3, R-SRV-100).
func (p *Process) attachLoop(ctx context.Context, base string) error {
	base = strings.TrimRight(base, "/")
	pin := &attachPin{base: base, static: len(p.cfg.Orchestrators) > 0}
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		// One client per stream, and every connection it dialed is closed when the stream
		// ends: a refused server must not keep a (pooled) connection of this worker (R-SRV-102).
		hc, conns := attachStreamClient(base)
		attached := false
		err := p.attachOnce(ctx, workerv1connect.NewWorkerControlServiceClient(hc, base+"/api"), pin, &attached)
		conns.closeAll()
		if ctx.Err() != nil {
			return nil
		}
		// A stream the server accepted (Welcome with a valid pin proof) restarts the backoff:
		// the next stream after it died is opened within a second, not after the backoff
		// that earlier failures had built up (A291).
		if attached || time.Since(start) > time.Minute {
			backoff = time.Second
		}
		if err != nil {
			p.cfg.Log.Info("worker: attach stream ended; reconnecting", "orchestrator", base, "err", err.Error())
		}
		if !sleep(ctx, attachJitter(backoff)) {
			return nil
		}
		backoff = min(2*backoff, 30*time.Second)
	}
	return nil
}

// attachJitter is a random duration in [d/2, d]: workers that lost their streams at the same
// moment (an orchestrator died) do not all retry in step.
func attachJitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}

// attachOnce runs one Attach stream: Hello, then wake-ups and live log subscriptions in,
// slot states and live chunks out, until the token is due for renewal or the stream fails.
// *attached is set once the server's Welcome proved the pinned key.
func (p *Process) attachOnce(ctx context.Context, cl workerv1connect.WorkerControlServiceClient, pin *attachPin, attached *bool) error {
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
	nonce, err := fleet.NewAttachNonce()
	if err != nil {
		return err
	}
	bs.RequestHeader().Set(fleet.AttachNonceHeader, nonce)
	var sendMu sync.Mutex
	send := func(m *workerv1.AttachRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return bs.Send(m)
	}
	defer func() {
		// Cancel first: CloseResponse drains the response body, which on a dead or refused
		// connection would wait for the server; a cancelled stream returns at once.
		cancel()
		sendMu.Lock()
		_ = bs.CloseRequest()
		sendMu.Unlock()
		_ = bs.CloseResponse()
	}()
	w := p.cur.Load()
	hello := &workerv1.Hello{WorkerId: p.id.ID(), Platform: p.id.Profile.Platform, SlotsTotal: int32(p.cfg.Slots)}
	if w != nil {
		hello.SlotsFree, hello.ClassIds = int32(w.FreeSlots()), w.Classes()
	}
	if err := send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Hello{Hello: hello}}); err != nil {
		// A Send error wrapping io.EOF means the server already ended the stream (e.g. it
		// refused the token); connect hands out the server's error from Receive (A170).
		if errors.Is(err, io.EOF) {
			if _, rerr := bs.Receive(); rerr != nil {
				return fmt.Errorf("hello: %w", rerr)
			}
		}
		return fmt.Errorf("hello: %w", err)
	}
	first, err := bs.Receive()
	if err != nil {
		return err
	}
	if first.GetWelcome() == nil {
		return fmt.Errorf("first message is %T, want Welcome", first.GetMsg())
	}
	if err := p.checkPin(ctx, pin, first.GetWelcome().GetOrchestratorId(), bs.ResponseHeader().Get(fleet.AttachProofHeader), nonce); err != nil {
		p.cfg.Log.Warn("worker: orchestrator refused: it does not prove its pinned key", "orchestrator", pin.base, "err", err.Error())
		return err
	}
	*attached = true
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
		// Every slotEvery, and at once when the free count changes (checked every
		// slotPoll): the Hello of a worker whose job loop has not started yet says 0 free
		// slots, and the orchestrator wakes nobody for jobs enqueued before the next state.
		t := time.NewTicker(slotPoll)
		defer t.Stop()
		sent, last := time.Now(), hello.SlotsFree // what the Hello said
		for {
			select {
			case <-sctx.Done():
				return nil
			case <-t.C:
			}
			w := p.cur.Load()
			if w == nil {
				continue
			}
			free := int32(w.FreeSlots())
			if free == last && time.Since(sent) < slotEvery {
				continue
			}
			if err := send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Slots{Slots: &workerv1.SlotState{SlotsFree: free}}}); err != nil {
				cancel()
				return nil
			}
			sent, last = time.Now(), free
		}
	}))
	// Silence watchdog: every received message re-arms it; when it fires the stream is
	// cancelled, Receive fails and attachLoop reconnects.
	var silent atomic.Bool
	quiet := time.AfterFunc(attachSilence, func() { silent.Store(true); cancel() })
	defer quiet.Stop()
	for {
		m, err := bs.Receive()
		if err != nil {
			if silent.Load() {
				return fmt.Errorf("no message from the orchestrator for %v: %w", attachSilence, errAttachSilent)
			}
			if sctx.Err() != nil || connect.CodeOf(err) == connect.CodeCanceled {
				return nil
			}
			return err
		}
		quiet.Reset(attachSilence)
		if ls != nil && ls.Handle(m) {
			continue
		}
		switch x := m.Msg.(type) {
		case *workerv1.AttachResponse_Wake:
			if w := p.cur.Load(); w != nil {
				w.Wake(x.Wake.ClassId, x.Wake.Slot, x.Wake.JobId)
			}
		case *workerv1.AttachResponse_CancelJob:
			if w := p.cur.Load(); w != nil {
				w.CheckJob(x.CancelJob.GetJobId())
			}
		case *workerv1.AttachResponse_Drain:
			if w := p.cur.Load(); w != nil {
				w.SetDraining(x.Drain.GetDrain())
			}
		case *workerv1.AttachResponse_Ping:
			_ = send(&workerv1.AttachRequest{Msg: &workerv1.AttachRequest_Pong{Pong: &workerv1.Pong{UnixMs: x.Ping.UnixMs}}})
		}
		// CancelJob and Drain are hints: the job watch then reads the cancel flag on job/<id> at
		// once; the worker record's state (draining/active, read every RecordRefresh) stays the
		// truth for draining.
	}
}
