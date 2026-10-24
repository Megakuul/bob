package fleet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// regLab is a fleet with registrars (orchestrators) and the means to craft workers.
type regLab struct {
	*lab
	ctx context.Context
}

func newRegLab(t *testing.T) *regLab {
	t.Helper()
	l := newLab(t)
	if l == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return &regLab{lab: l, ctx: ctx}
}

func (r *regLab) registrar(name string, key ed25519.PrivateKey) *orchestrator.Registrar {
	r.t.Helper()
	if key == nil {
		key = r.priv
	}
	reg, err := orchestrator.NewRegistrar(orchestrator.RegistrarConfig{
		Control: r.control, Fleet: r.fleet, Name: name, Key: key, Kid: "k1",
		TokenTTL: time.Hour, RegWindow: 24 * time.Hour, Heartbeat: 15 * time.Second, MaxPerPass: 50, Now: r.clk.Now,
	})
	if err != nil {
		if env.Red(r.t, err) {
			return nil
		}
		r.t.Fatal(err)
	}
	return reg
}

func (r *regLab) identity() *worker.Identity {
	r.t.Helper()
	id, err := worker.NewIdentity(rand.Reader)
	if err != nil {
		if env.Red(r.t, err) {
			return nil
		}
		r.t.Fatal(err)
	}
	return id
}

// enrol registers id and lets reg approve it; it returns the opened token.
func (r *regLab) enrol(reg *orchestrator.Registrar, id *worker.Identity) *worker.Token {
	r.t.Helper()
	if _, err := worker.Register(r.ctx, r.fleet, id, r.clk.Now()); err != nil {
		r.t.Fatalf("Register: %v", err)
	}
	res, err := reg.Pass(r.ctx)
	if err != nil || res.Approved != 1 {
		r.t.Fatalf("registrar pass = %+v, %v; want 1 approval", res, err)
	}
	return r.open(id)
}

func (r *regLab) open(id *worker.Identity) *worker.Token {
	r.t.Helper()
	s, err := worker.FetchSealed(r.ctx, r.fleet, id.ID())
	if err != nil {
		r.t.Fatalf("FetchSealed(%s): %v", id.ID(), err)
	}
	tok, err := id.OpenToken(s)
	if err != nil {
		r.t.Fatalf("OpenToken: %v", err)
	}
	return tok
}

// call authenticates one request made with tok and PoP by pop's identity.
func (r *regLab) call(reg *orchestrator.Registrar, tok *worker.Token, pop *worker.Identity, path string) (string, error) {
	return reg.Authenticate(r.ctx, tok.Authorization(), tok.PoP(pop, "POST", path, r.clk.Now()), "POST", path)
}

const popInvalid = "unauthenticated: proof of possession invalid"

func wantErr(t *testing.T, what string, err error, contains ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted, want a refusal containing %q", what, contains)
		return
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("%s: error %q does not contain %q", what, err, c)
		}
	}
}

// TestRFLEET002Registration walks the registration checks of docs/spikes/s11-workers.md §3.4:
// approval, sealed tokens, proof of possession, forged and replayed registrations.
func TestRFLEET002Registration(t *testing.T) {
	parity.Covers(t, "R-FLEET-002")
	r := newRegLab(t)
	if r == nil {
		return
	}
	reg := r.registrar("o1", nil)
	if reg == nil {
		return
	}
	victim, attacker := r.identity(), r.identity()

	t.Run("register, reconcile, open, authenticate", func(t *testing.T) {
		if _, err := worker.Register(r.ctx, r.fleet, victim, r.clk.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.FetchSealed(r.ctx, r.fleet, victim.ID()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("token visible before the registrar approved: %v, want ErrNotFound", err)
		}
		if res, err := reg.Pass(r.ctx); err != nil || res.Approved != 1 {
			t.Fatalf("registrar pass = %+v, %v", res, err)
		}
		tok := r.open(victim)
		if wid, err := r.call(reg, tok, victim, "/bob.worker.v1.WorkerControlService/Attach"); err != nil || wid != victim.ID() {
			t.Errorf("token + PoP = %q, %v; want the worker id", wid, err)
		}
	})

	t.Run("second create of the same wreg", func(t *testing.T) {
		if _, err := worker.Register(r.ctx, r.fleet, victim, r.clk.Now()); !errors.Is(err, store.ErrExists) {
			t.Errorf("second Register of the same worker = %v, want store.ErrExists", err)
		}
	})

	tok := r.open(victim)
	t.Run("pop replay and other path", func(t *testing.T) {
		path := "/bob.worker.v1.WorkerControlService/GetCheckoutCredential"
		pop := tok.PoP(victim, "POST", path, r.clk.Now())
		if _, err := reg.Authenticate(r.ctx, tok.Authorization(), pop, "POST", path); err != nil {
			t.Fatalf("first use of a PoP: %v", err)
		}
		_, err := reg.Authenticate(r.ctx, tok.Authorization(), pop, "POST", path)
		wantErr(t, "replayed PoP", err, "unauthenticated:", "replay")
		pop2 := tok.PoP(victim, "POST", path, r.clk.Now())
		_, err = reg.Authenticate(r.ctx, tok.Authorization(), pop2, "POST", "/bob.worker.v1.WorkerControlService/Attach")
		wantErr(t, "PoP made for another path", err, popInvalid)
	})

	t.Run("stolen token with the attacker's PoP", func(t *testing.T) {
		_, err := r.call(reg, tok, attacker, "/bob.worker.v1.WorkerControlService/Attach")
		wantErr(t, "a worker's token used with another worker's key", err, popInvalid)
	})

	t.Run("attacker opens the victim's sealed token", func(t *testing.T) {
		s, err := worker.FetchSealed(r.ctx, r.fleet, victim.ID())
		if err != nil {
			t.Fatal(err)
		}
		_, err = attacker.OpenToken(s)
		wantErr(t, "foreign OpenToken", err, "message authentication failed")
		for name, moved := range map[string]*worker.Sealed{
			"another serial":     {Bytes: s.Bytes, Serial: s.Serial + 1, Gen: s.Gen, Expiry: s.Expiry},
			"another generation": {Bytes: s.Bytes, Serial: s.Serial, Gen: s.Gen + 1, Expiry: s.Expiry},
		} {
			_, err := victim.OpenToken(moved)
			wantErr(t, "sealed token at "+name, err, "message authentication failed")
		}
	})

	t.Run("token signed with another key", func(t *testing.T) {
		_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
		other := r.registrar("o-other", otherKey)
		_, err := r.call(other, tok, victim, "/bob.worker.v1.WorkerControlService/Attach")
		wantErr(t, "token of another orchestrator key", err, "unauthenticated:")
	})

	t.Run("replayed registration under another id", func(t *testing.T) {
		doc, err := worker.RawReg(r.ctx, r.fleet, victim.ID())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := worker.RegisterRaw(r.ctx, r.fleet, attacker.ID(), doc, r.clk.Now()); err != nil {
			t.Fatal(err)
		}
		res, err := reg.Pass(r.ctx)
		if err != nil || res.Approved != 0 || len(res.Rejected) != 1 {
			t.Fatalf("pass = %+v, %v; want the replay rejected", res, err)
		}
		wantErr(t, "replayed document", errors.New(res.Rejected[0]), "worker registration rejected:")
		if _, err := worker.FetchSealed(r.ctx, r.fleet, attacker.ID()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a rejected registration got a worker record: %v", err)
		}
	})

	t.Run("victim's signing key with the attacker's KEM key", func(t *testing.T) {
		ev := r.identity()
		doc, err := worker.ForgeRegBytes(victim, attacker, ev, r.clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := worker.RegisterRaw(r.ctx, r.fleet, ev.ID(), doc, r.clk.Now()); err != nil {
			t.Fatal(err)
		}
		if res, err := reg.Pass(r.ctx); err != nil || res.Approved != 0 || len(res.Rejected) != 1 {
			t.Errorf("pass = %+v, %v; want the forged key pair rejected", res, err)
		}
	})

	t.Run("registration older than the window", func(t *testing.T) {
		old := r.identity()
		if _, err := worker.Register(r.ctx, r.fleet, old, r.clk.Now().Add(-25*time.Hour)); err != nil {
			t.Fatal(err)
		}
		res, err := reg.Pass(r.ctx)
		if err != nil || res.Approved != 0 || len(res.Rejected) != 1 {
			t.Errorf("pass = %+v, %v; want the 25 h old registration rejected", res, err)
		}
	})

	t.Run("overwrite of wreg/<victim> is harmless", func(t *testing.T) {
		// S3 policy lets any worker PUT wreg/; the keys were copied into worker/<wid> at approval.
		resp, err := r.be.FS.Do(r.ctx, r.be.FS.Root, "PUT", "/"+r.fname+"/wreg/"+victim.ID(), nil, []byte("attacker was here"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("overwrite of wreg/<victim> = %d", resp.StatusCode)
		}
		if _, err := reg.Pass(r.ctx); err != nil {
			t.Fatal(err)
		}
		again := r.open(victim)
		if _, err := r.call(reg, again, victim, "/bob.worker.v1.WorkerControlService/Attach"); err != nil {
			t.Errorf("victim after an overwrite of its wreg record: %v", err)
		}
	})
}

// TestRFLEET002TokenLifecycle: rotation, overlap, expiry, revocation.
func TestRFLEET002TokenLifecycle(t *testing.T) {
	parity.Covers(t, "R-FLEET-002")
	r := newRegLab(t)
	if r == nil {
		return
	}
	reg := r.registrar("o1", nil)
	if reg == nil {
		return
	}
	attach := "/bob.worker.v1.WorkerControlService/Attach"

	t.Run("rotation at half-life only with a fresh signed heartbeat", func(t *testing.T) {
		id := r.identity()
		r.enrol(reg, id)
		r.clk.Advance(31 * time.Minute) // past half of the 1 h TTL
		if n, err := reg.Rotate(r.ctx); err != nil || n != 0 {
			t.Fatalf("Rotate without a heartbeat = %d, %v; want no rotation", n, err)
		}
		if s, _ := worker.FetchSealed(r.ctx, r.fleet, id.ID()); s == nil || s.Serial != 1 {
			t.Fatalf("serial after a refused rotation = %+v, want 1", s)
		}
		if err := worker.Heartbeat(r.ctx, r.fleet, id, 1, nil, r.clk.Now()); err != nil {
			t.Fatal(err)
		}
		if n, err := reg.Rotate(r.ctx); err != nil || n != 1 {
			t.Fatalf("Rotate with a fresh heartbeat = %d, %v; want 1", n, err)
		}
		if s, _ := worker.FetchSealed(r.ctx, r.fleet, id.ID()); s == nil || s.Serial != 2 {
			t.Errorf("serial after rotation = %+v, want 2", s)
		}
	})

	t.Run("overlap and forced rotations", func(t *testing.T) {
		id := r.identity()
		t1 := r.enrol(reg, id)
		if err := reg.ForceRotate(r.ctx, id.ID()); err != nil {
			t.Fatal(err)
		}
		t2 := r.open(id)
		if _, err := r.call(reg, t1, id, attach); err != nil {
			t.Errorf("previous serial during the overlap: %v", err)
		}
		if err := reg.ForceRotate(r.ctx, id.ID()); err != nil {
			t.Fatal(err)
		}
		_, err := r.call(reg, t1, id, attach)
		wantErr(t, "serial 1 after two rotations", err, "unauthenticated:", "superseded serial 1 (current 3)")
		if _, err := r.call(reg, t2, id, attach); err != nil {
			t.Errorf("previous serial 2 at current 3: %v", err)
		}
	})

	t.Run("dead worker: the token expires and is not reissued", func(t *testing.T) {
		id := r.identity()
		tok := r.enrol(reg, id)
		r.clk.Advance(61 * time.Minute)
		if n, _ := reg.Rotate(r.ctx); n != 0 {
			t.Errorf("Rotate reissued a token for a worker without heartbeat")
		}
		_, err := r.call(reg, tok, id, attach)
		wantErr(t, "expired token", err, "unauthenticated:", "expired")
		if s, _ := worker.FetchSealed(r.ctx, r.fleet, id.ID()); s == nil || s.Serial != 1 {
			t.Errorf("a dead worker got a new token: %+v", s)
		}
	})

	t.Run("revoke: refused on the next request, never reissued, within 10 s everywhere", func(t *testing.T) {
		id := r.identity()
		tok := r.enrol(reg, id)
		other := r.registrar("o2", nil) // a second orchestrator with its own record cache
		if _, err := r.call(other, tok, id, attach); err != nil {
			t.Fatal(err)
		}
		if err := reg.Revoke(r.ctx, id.ID(), "compromised"); err != nil {
			t.Fatal(err)
		}
		_, err := r.call(reg, tok, id, attach)
		wantErr(t, "revoked worker on the revoking orchestrator", err, "unauthenticated:", "worker revoked")
		r.clk.Advance(11 * time.Second) // record cache <= 10 s
		_, err = r.call(other, tok, id, attach)
		wantErr(t, "revoked worker on another orchestrator after the cache time", err, "worker revoked")
		if err := reg.ForceRotate(r.ctx, id.ID()); err == nil {
			t.Errorf("ForceRotate re-issued a token for a revoked worker")
		}
		r.clk.Advance(40 * time.Minute)
		_ = worker.Heartbeat(r.ctx, r.fleet, id, 1, nil, r.clk.Now())
		if n, _ := reg.Rotate(r.ctx); n != 0 {
			t.Errorf("Rotate re-issued a token for a revoked worker")
		}
		// The revoked host registers again with new keys: approved (the credential is the proof).
		fresh := r.identity()
		r.enrol(reg, fresh)
	})
}

// TestRFLEET002ConcurrentRegistrars: 3 orchestrators approve 20 concurrent registrations, each
// exactly once, and every token is usable; one pass approves at most 50 of 100 pending.
func TestRFLEET002ConcurrentRegistrars(t *testing.T) {
	parity.Covers(t, "R-FLEET-002", "R-FLEET-001")
	r := newRegLab(t)
	if r == nil {
		return
	}
	regs := []*orchestrator.Registrar{r.registrar("o1", nil), r.registrar("o2", nil), r.registrar("o3", nil)}
	if regs[0] == nil {
		return
	}
	var ids []*worker.Identity
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		id := r.identity()
		ids = append(ids, id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := worker.Register(r.ctx, r.fleet, id, r.clk.Now()); err != nil {
				mu.Lock()
				t.Errorf("Register: %v", err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	approved := 0
	for round := 0; round < 4; round++ {
		for _, reg := range regs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := reg.Pass(r.ctx)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					t.Errorf("Pass: %v", err)
				}
				approved += res.Approved
			}()
		}
		wg.Wait()
	}
	if approved != 20 {
		t.Errorf("3 registrars approved %d registrations in total, want exactly 20 (each once)", approved)
	}
	for _, id := range ids {
		tok := r.open(id)
		for _, reg := range regs {
			if wid, err := r.call(reg, tok, id, "/bob.worker.v1.WorkerControlService/Attach"); err != nil || wid != id.ID() {
				t.Errorf("token of %s not usable at every orchestrator: %q, %v", id.ID(), wid, err)
			}
		}
	}
}

// TestRFLEET002RegistrationBacklog: a pass approves at most 50 registrations (SPEC §05.6.3);
// 100 pending are all approved over two passes.
func TestRFLEET002RegistrationBacklog(t *testing.T) {
	parity.Covers(t, "R-FLEET-002")
	r := newRegLab(t)
	if r == nil {
		return
	}
	reg := r.registrar("o1", nil)
	if reg == nil {
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		id := r.identity()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := worker.Register(r.ctx, r.fleet, id, r.clk.Now()); err != nil {
				t.Errorf("Register: %v", err)
			}
		}()
	}
	wg.Wait()
	first, err := reg.Pass(r.ctx)
	if err != nil || first.Approved != 50 {
		t.Fatalf("first pass approved %d (%v), want exactly the cap of 50", first.Approved, err)
	}
	second, err := reg.Pass(r.ctx)
	if err != nil || second.Approved != 50 {
		t.Errorf("second pass approved %d (%v), want the remaining 50", second.Approved, err)
	}
	r.be.Proxy.AssertNoList(t, r.fname, r.cname)
}

// TestRFLEET002ForgedResultsAndSpecs: a job result signed by a non-owner is ignored (metric
// bob_fleet_forged_results_total), and a JobSpec with a bad signature is never run: the worker
// marks its slot stale.
func TestRFLEET002ForgedResultsAndSpecs(t *testing.T) {
	parity.Covers(t, "R-FLEET-002")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	g := newLedger()
	ev := &events{}
	release := make(chan struct{})
	cfg := l.workerCfg("w1", func(ctx context.Context, c worker.Claim) worker.Outcome {
		g.note("w1", c)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return worker.Outcome{Status: "ok"}
	})
	cfg.Events = ev.add
	if l.start(cfg) == nil {
		return
	}

	// Bad spec signature: the slot is marked stale and nothing runs.
	_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
	bad := *l.q
	bad.Key = wrongKey
	slot, err := bad.Enqueue(ctx, l.job("bad-spec"), orchestrator.JobFirst)
	if env.Red(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !env.Wait(20*time.Second, func() bool { got, _ := l.q.SlotTaken(ctx, slot); return got == "stale" }) {
		got, _ := l.q.SlotTaken(ctx, slot)
		t.Errorf("slot of a job with a bad spec signature is %q, want stale", got)
	}
	if r := g.of("bad-spec"); len(r) != 0 {
		t.Errorf("a JobSpec with a bad signature was run: %+v", r)
	}
	if len(ev.of("stale-spec")) == 0 {
		t.Errorf("no stale-spec event for the bad spec")
	}

	// Forged result: w1 holds j1; another worker overwrites job/j1 with a "succeeded" result
	// in w1's name, signed by its own key.
	if _, err := l.q.Enqueue(ctx, l.job("j1"), orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	v := l.waitState("j1", 20*time.Second, "claimed", "running")
	if v == nil || v.Owner != "w1" {
		t.Fatalf("j1 not held by w1: %+v", v)
	}
	forger, err := worker.NewIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	before := orchestrator.ForgedResults()
	if err := worker.ForgeResult(ctx, l.fleet, forger, "w1", "j1", v.Epoch); err != nil {
		t.Fatal(err)
	}
	ok, err := l.q.Finalise(ctx, "j1")
	if err != nil || ok {
		t.Errorf("Finalise of a forged result = accepted %v, %v; want it ignored", ok, err)
	}
	if got := orchestrator.ForgedResults(); got != before+1 {
		t.Errorf("bob_fleet_forged_results_total went %d -> %d, want +1", before, got)
	}
	close(release)
	if v := l.waitState("j1", 20*time.Second, "succeeded"); v == nil || v.State != "succeeded" || v.Owner != "w1" {
		t.Errorf("the true owner's result was not accepted after the forgery: %+v", v)
	}
}

// TestRFLEET002AttachIsolation: worker A's Attach cannot subscribe to, cancel, or report on
// worker B's jobs.
func TestRFLEET002AttachIsolation(t *testing.T) {
	parity.Covers(t, "R-FLEET-002")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	mk := func(id string) *worker.Worker {
		cfg := l.workerCfg(id, func(ctx context.Context, c worker.Claim) worker.Outcome {
			select {
			case <-hold:
			case <-ctx.Done():
			}
			return worker.Outcome{Status: "ok"}
		})
		cfg.Slots = 1
		return l.start(cfg)
	}
	if mk("wa") == nil {
		return
	}
	if _, err := l.q.Enqueue(ctx, l.job("job-a"), orchestrator.JobFirst); err != nil {
		if env.Red(t, err) {
			return
		}
		t.Fatal(err)
	}
	va := l.waitState("job-a", 20*time.Second, "claimed", "running")
	if mk("wb") == nil {
		return
	}
	if _, err := l.q.Enqueue(ctx, l.job("job-b"), orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	vb := l.waitState("job-b", 20*time.Second, "claimed", "running")
	if va == nil || vb == nil || va.Owner != "wa" || vb.Owner != "wb" {
		t.Fatalf("jobs not held as expected: %+v %+v", va, vb)
	}
	h := orchestrator.NewHub(l.q)
	a, b := h.Attach("wa"), h.Attach("wb")
	if err := h.Subscribe(ctx, "job-b"); err != nil {
		if env.Red(t, err) {
			return
		}
		t.Fatalf("Subscribe: %v", err)
	}
	if err := h.CancelJob(ctx, "job-b"); err != nil {
		t.Fatal(err)
	}
	got := func(c *orchestrator.Conn) []string {
		var out []string
		deadline := time.After(time.Second)
		for {
			select {
			case m := <-c.Recv():
				out = append(out, m)
			case <-deadline:
				return out
			}
		}
	}
	if msgs := got(a); len(msgs) != 0 {
		t.Errorf("worker A received messages about worker B's job: %v", msgs)
	}
	msgs := got(b)
	if len(msgs) != 2 || msgs[0] != "subscribe job-b" || msgs[1] != "cancel job-b" {
		t.Errorf("worker B received %v, want [subscribe job-b cancel job-b]", msgs)
	}
	const denied = "permission_denied: worker holds no lease on job job-b"
	if err := a.JobEvent(ctx, "job-b", vb.Epoch, "abandoned"); err == nil || err.Error() != denied {
		t.Errorf("A reporting an event for B's job: %v, want %q", err, denied)
	}
	if err := a.LiveChunk(ctx, "job-b", vb.Epoch); err == nil || err.Error() != denied {
		t.Errorf("A sending log lines for B's job: %v, want %q", err, denied)
	}
	if v := l.view("job-b"); v == nil || v.State == "queued" {
		t.Errorf("A's forged event changed B's job: %+v", v)
	}
	if err := b.JobEvent(ctx, "job-b", vb.Epoch, "running"); err != nil {
		t.Errorf("B reporting its own job: %v", err)
	}
}
