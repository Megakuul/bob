package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/dynamitedb"

	"bobspike/s11/internal/reg"
	"bobspike/s11/internal/s3x"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type checker struct {
	server string
	fails  int
}

func (c *checker) check(name string, ok bool, detail string) {
	mark := "pass"
	if !ok {
		mark = "**FAIL**"
		c.fails++
	}
	fmt.Printf("| %s | %s | %s |\n", name, detail, mark)
}

func runRegflow(ctx context.Context, server, endpoint string, root, worker, orch s3x.Creds) int {
	wc := dynamitedb.NewFromClient(s3x.Client(endpoint, worker), "bob-fleet")
	oc := dynamitedb.NewFromClient(s3x.Client(endpoint, orch), "bob-fleet")
	ctrl := dynamitedb.NewFromClient(s3x.Client(endpoint, orch), "bob-control")
	rawW := s3x.Client(endpoint, worker)
	clearFleet(ctx, s3x.Client(endpoint, orch))

	clk := &clock{t: time.Now()}
	newOrch := func(kid string, seed []byte) *reg.Orchestrator {
		k := ed25519.NewKeyFromSeed(seed)
		return &reg.Orchestrator{Fleet: oc, KID: kid, Key: k, Pubs: map[string]ed25519.PublicKey{kid: k.Public().(ed25519.PublicKey)},
			TTL: time.Hour, HBStale: 2 * time.Minute, Now: clk.Now}
	}
	// orchestrator signing key lives in the control bucket (orchestrator-only).
	seed := make([]byte, 32)
	rand.Read(seed)
	must(dynamitedb.Put(ctx, ctrl, &reg.OrchKey{KID: dynamitedb.Key("k1"), Seed: dynamitedb.Set(seed)}))
	ok1, err := dynamitedb.Get(ctx, ctrl, &reg.OrchKey{KID: dynamitedb.Key("k1")})
	must(err)
	o := newOrch("k1", ok1.Seed.Value())
	_, werr := dynamitedb.Get(ctx, dynamitedb.NewFromClient(s3x.Client(endpoint, worker), "bob-control"), &reg.OrchKey{KID: dynamitedb.Key("k1")})

	c := &checker{server: server}
	fmt.Printf("\n### %s: registration flow (dynamitedb %s)\n\n| check | detail | |\n|---|---|---|\n", server, "v0.6.1")
	c.check("worker cannot read orchestrator signing key (control/okey/k1)", werr != nil && strings.Contains(s3x.Class(unwrapS3(werr)), "DENIED"), s3x.Class(unwrapS3(werr)))

	// --- happy path, timed
	t0 := time.Now()
	k, _ := reg.NewWorkerKeys(reg.SuiteMLKEM768X25519)
	must(reg.Register(ctx, wc, k, "aarch64-linux", []string{"kvm=no"}, clk.Now()))
	tReg := time.Since(t0)
	_, _, ferr := reg.FetchToken(ctx, wc, k)
	c.check("token not visible before reconcile", errors.Is(ferr, dynamitedb.ErrNotFound), fmt.Sprint(ferr))
	t1 := time.Now()
	st, err := o.Reconcile(ctx)
	must(err)
	tRec := time.Since(t1)
	t2 := time.Now()
	env, serial, err := reg.FetchToken(ctx, wc, k)
	tFetch := time.Since(t2)
	c.check("happy path: approve + open sealed token", err == nil && st.Approved == 1 && serial == 1, fmt.Sprintf("approved=%d serial=%d err=%v; register %s, reconcile(1 reg) %s, fetch+open %s [8c/15G, shared host]", st.Approved, serial, err, ms(tReg), ms(tRec), ms(tFetch)))
	rq := reg.Request{Method: "POST", Authority: "orch1.example:443", Path: "/bob.worker.v1.Worker/ClaimJob", Body: []byte(`{}`)}
	wid, err := o.Verify(ctx, env.Token, reg.PoP(k, env.Token, rq, clk.Now()), rq)
	c.check("token + PoP accepted by orchestrator", err == nil && wid == k.ID(), fmt.Sprint(wid, " ", err))
	pop := reg.PoP(k, env.Token, rq, clk.Now())
	o.Verify(ctx, env.Token, pop, rq)
	_, err = o.Verify(ctx, env.Token, pop, rq)
	c.check("same PoP replayed to the same orchestrator", errors.Is(err, reg.ErrPoP), fmt.Sprint(err))
	rq2 := rq
	rq2.Path = "/bob.worker.v1.Worker/CompleteJob"
	_, err = o.Verify(ctx, env.Token, reg.PoP(k, env.Token, rq, clk.Now()), rq2)
	c.check("PoP for another path rejected", errors.Is(err, reg.ErrPoP), fmt.Sprint(err))

	// --- duplicate create (worker restarts with same record) is harmless
	err = reg.Register(ctx, wc, k, "aarch64-linux", nil, clk.Now())
	c.check("second Create of same wreg -> ErrAlreadyExists", errors.Is(err, dynamitedb.ErrAlreadyExists), fmt.Sprint(err))

	// --- attacker = another legitimate worker credential holder
	atk, _ := reg.NewWorkerKeys(reg.SuiteMLKEM768X25519)
	// A. steal the victim's sealed record and try to open it
	rec, _ := dynamitedb.Get(ctx, wc, &reg.WorkerRecord{ID: dynamitedb.Key(k.ID())})
	_, err = atk.Open(k.ID(), rec.Gen.Value(), rec.Serial.Value(), rec.Sealed.Value())
	c.check("attacker reads worker/<victim> and tries to open sealed token", err != nil, fmt.Sprint(err))
	// B. replay the victim's registration bytes under a new key wreg/<other>
	vreg, _ := dynamitedb.Get(ctx, wc, &reg.Registration{ID: dynamitedb.Key(k.ID())})
	other := "w" + strings.Repeat("a", 26)
	must(dynamitedb.Create(ctx, wc, &reg.Registration{ID: dynamitedb.Key(other), Body: dynamitedb.Set(vreg.Body.Value()), Sig: dynamitedb.Set(vreg.Sig.Value())}))
	// C. victim's SigPub + attacker's KEM key (to receive the token), re-signed by attacker -> sig invalid
	forgeID, forgeBody := forgeReg(k.SigPub(), atk, clk.Now())
	must(dynamitedb.Create(ctx, wc, &reg.Registration{ID: dynamitedb.Key(forgeID), Body: dynamitedb.Set(forgeBody), Sig: dynamitedb.Set(ed25519.Sign(atk.Sig, append([]byte("bob-wreg-v1\x00"), forgeBody...)))}))
	// D. overwrite the victim's wreg/ (allowed by S3 policy: PutObject) with the attacker's own keys
	err = reg.RegisterAt(ctx, wc, atk, k.ID(), clk.Now())
	c.check("attacker overwrites wreg/<victim> (PutObject allowed by policy)", err == nil, fmt.Sprint(err))
	// E. delete the victim's registration
	_, derr := rawW.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("bob-fleet"), Key: aws.String("wreg/" + k.ID())})
	c.check("attacker deletes wreg/<victim>", s3x.Class(derr) == "DENIED", s3x.Class(derr))
	// F. write a forged worker record
	ferr = dynamitedb.Put(ctx, wc, &reg.WorkerRecord{ID: dynamitedb.Key(atk.ID()), State: dynamitedb.Set(reg.StateActive)})
	c.check("attacker writes worker/<self> (forged approval)", s3x.Class(unwrapS3(ferr)) == "DENIED", s3x.Class(unwrapS3(ferr)))
	// G. stale registration (created 25h ago)
	old, _ := reg.NewWorkerKeys(reg.SuiteX25519)
	must(reg.Register(ctx, wc, old, "aarch64-linux", nil, clk.Now().Add(-25*time.Hour)))

	st, err = o.Reconcile(ctx)
	must(err)
	c.check("reconcile after attacks", err == nil && st.Rejected == 3 && st.Approved == 0, fmt.Sprintf("approved=%d rejected=%d skipped=%d (replayed bytes, forged key mix, 25h-old reg rejected)", st.Approved, st.Rejected, st.Skipped))
	_, _, err = reg.FetchToken(ctx, wc, k)
	c.check("victim still opens its token after wreg/ overwrite (keys copied into worker/)", err == nil, fmt.Sprint(err))
	// H. stolen token without the key: attacker signs PoP with own key
	_, err = o.Verify(ctx, env.Token, reg.PoP(atk, env.Token, rq, clk.Now()), rq)
	c.check("stolen token + attacker's PoP", errors.Is(err, reg.ErrPoP), fmt.Sprint(err))
	// I. forged token signed by a non-orchestrator key
	fake := newOrch("k1", make([]byte, 32))
	ft, _ := fake.SignToken(reg.Claims{V: 1, Aud: "bob-worker-api", KID: "k1", WID: atk.ID(), Exp: clk.Now().Add(time.Hour).Unix()})
	_, err = o.Verify(ctx, ft, reg.PoP(atk, ft, rq, clk.Now()), rq)
	c.check("token forged with wrong key", errors.Is(err, reg.ErrTokenInvalid), fmt.Sprint(err))
	// J. sealed token moved to another worker record (root, simulating policy failure)
	k2, _ := reg.NewWorkerKeys(reg.SuiteMLKEM768X25519)
	must(reg.Register(ctx, wc, k2, "aarch64-linux", nil, clk.Now()))
	o.Reconcile(ctx)
	r2, _ := dynamitedb.Get(ctx, oc, &reg.WorkerRecord{ID: dynamitedb.Key(k2.ID())})
	_, err = k2.Open(k2.ID(), r2.Gen.Value(), r2.Serial.Value(), rec.Sealed.Value())
	c.check("ciphertext of worker A opened by B (info binds wid/gen/serial)", err != nil, fmt.Sprint(err))
	_, err = k.Open(k.ID(), rec.Gen.Value(), rec.Serial.Value()+1, rec.Sealed.Value())
	c.check("ciphertext replayed at another serial", err != nil, fmt.Sprint(err))

	// --- rotation
	must(reg.Beat(ctx, wc, k, clk.Now()))
	clk.Add(31 * time.Minute)
	must(reg.Beat(ctx, wc, k, clk.Now()))
	st, _ = o.Reconcile(ctx)
	env2, serial2, err := reg.FetchToken(ctx, wc, k)
	c.check("rotation at half-life for live worker", err == nil && serial2 == 2 && st.Rotated >= 1, fmt.Sprintf("rotated=%d serial=%d", st.Rotated, serial2))
	c.check("k2 without heartbeat is not rotated", func() bool {
		r, _ := dynamitedb.Get(ctx, oc, &reg.WorkerRecord{ID: dynamitedb.Key(k2.ID())})
		return r.Serial.Value() == 1
	}(), "serial stays 1")
	_, err = o.Verify(ctx, env.Token, reg.PoP(k, env.Token, rq, clk.Now()), rq)
	c.check("previous token (serial 1) still accepted during overlap", err == nil, fmt.Sprint(err))
	clk.Add(31 * time.Minute)
	must(reg.Beat(ctx, wc, k, clk.Now()))
	o.Reconcile(ctx)
	_, err = o.Verify(ctx, env.Token, reg.PoP(k, env.Token, rq, clk.Now()), rq)
	c.check("serial 1 rejected after second rotation", errors.Is(err, reg.ErrTokenInvalid), fmt.Sprint(err))
	_, err = o.Verify(ctx, env2.Token, reg.PoP(k, env2.Token, rq, clk.Now()), rq)
	c.check("serial 2 accepted", err == nil, fmt.Sprint(err))
	// forced rotation (suspected leak): serial 2 still valid, serial 2 dies on the second forced rotation
	must(reg.Beat(ctx, wc, k, clk.Now()))
	envC, _, _ := reg.FetchToken(ctx, wc, k)
	must(o.ForceRotate(ctx, k.ID()))
	_, err = o.Verify(ctx, envC.Token, reg.PoP(k, envC.Token, rq, clk.Now()), rq)
	okPrev := err == nil
	must(o.ForceRotate(ctx, k.ID()))
	_, err = o.Verify(ctx, envC.Token, reg.PoP(k, envC.Token, rq, clk.Now()), rq)
	c.check("forced rotation x2 kills an unexpired leaked token", okPrev && err != nil && strings.Contains(err.Error(), "superseded"), fmt.Sprint(err))
	clk.Add(2 * time.Hour) // no heartbeat: token expires
	o.Reconcile(ctx)
	env3, _, _ := reg.FetchToken(ctx, wc, k)
	_, err = o.Verify(ctx, env3.Token, reg.PoP(k, env3.Token, rq, clk.Now()), rq)
	c.check("dead worker: token expires, no rotation", errors.Is(err, reg.ErrTokenInvalid), fmt.Sprint(err))

	// --- revocation
	must(reg.Beat(ctx, wc, k, clk.Now()))
	o.Reconcile(ctx)
	env4, _, err := reg.FetchToken(ctx, wc, k)
	_, err = o.Verify(ctx, env4.Token, reg.PoP(k, env4.Token, rq, clk.Now()), rq)
	c.check("revived worker gets fresh token", err == nil, fmt.Sprint(err))
	t3 := time.Now()
	must(o.Revoke(ctx, k.ID()))
	tRevoke := time.Since(t3)
	_, err = o.Verify(ctx, env4.Token, reg.PoP(k, env4.Token, rq, clk.Now()), rq)
	c.check("revoked: valid token rejected on next request", errors.Is(err, reg.ErrRevoked), fmt.Sprintf("%v (revoke write %s)", err, ms(tRevoke)))
	_, _, err = reg.FetchToken(ctx, wc, k)
	c.check("revoked: worker sees revoked state", errors.Is(err, reg.ErrRevoked), fmt.Sprint(err))
	clk.Add(40 * time.Minute)
	must(reg.Beat(ctx, wc, k, clk.Now()))
	st, _ = o.Reconcile(ctx)
	r3, _ := dynamitedb.Get(ctx, oc, &reg.WorkerRecord{ID: dynamitedb.Key(k.ID())})
	c.check("revoked worker is never reissued", r3.State.Value() == reg.StateRevoked && len(r3.Sealed.Value()) == 0, "state="+r3.State.Value())
	k3, _ := reg.NewWorkerKeys(reg.SuiteMLKEM768X25519)
	must(reg.Register(ctx, wc, k3, "aarch64-linux", nil, clk.Now()))
	st, _ = o.Reconcile(ctx)
	_, _, err = reg.FetchToken(ctx, wc, k3)
	c.check("LIMIT: revoked host re-registers with new keys and is approved", err == nil, "fleet credential = proof of being a worker (R-FLEET-001)")

	// --- concurrent orchestrators
	clearFleet(ctx, s3x.Client(endpoint, orch))
	const nW, nO = 20, 3
	keys := make([]*reg.WorkerKeys, nW)
	for i := range keys {
		keys[i], _ = reg.NewWorkerKeys(reg.SuiteMLKEM768X25519)
		must(reg.Register(ctx, wc, keys[i], "aarch64-linux", nil, clk.Now()))
	}
	var wg sync.WaitGroup
	sts := make([]reg.Stats, nO)
	errs := make([]error, nO)
	t4 := time.Now()
	for i := range nO {
		wg.Add(1)
		go func() {
			defer wg.Done()
			oi := newOrch("k1", ok1.Seed.Value())
			sts[i], errs[i] = oi.Reconcile(ctx)
		}()
	}
	wg.Wait()
	tConc := time.Since(t4)
	approved, lost := 0, 0
	for i := range nO {
		approved += sts[i].Approved
		lost += sts[i].LostRace + sts[i].Skipped
		if errs[i] != nil {
			fmt.Println("orchestrator error:", errs[i])
		}
	}
	opened := 0
	for _, kk := range keys {
		if e, _, err := reg.FetchToken(ctx, wc, kk); err == nil {
			if _, err := o.Verify(ctx, e.Token, reg.PoP(kk, e.Token, rq, clk.Now()), rq); err == nil {
				opened++
			}
		}
	}
	c.check(fmt.Sprintf("%d orchestrators reconcile %d registrations concurrently", nO, nW), approved == nW && opened == nW,
		fmt.Sprintf("approved=%d (exactly once each), lost race/skipped=%d, tokens usable=%d, wall %s", approved, lost, opened, ms(tConc)))

	// --- registration flood cost
	for i := range 100 {
		kk, _ := reg.NewWorkerKeys(reg.SuiteMLKEM768X25519)
		_ = i
		must(reg.Register(ctx, wc, kk, "aarch64-linux", nil, clk.Now()))
	}
	t5 := time.Now()
	st, err = o.Reconcile(ctx)
	tFlood := time.Since(t5)
	t6 := time.Now()
	st2, _ := o.Reconcile(ctx)
	tIdle := time.Since(t6)
	c.check("flood: 100 new registrations in one pass", err == nil && st.Approved == 100,
		fmt.Sprintf("pass %s (%s/reg); idle pass over %d records %s [8c/15G, shared host]", ms(tFlood), ms(tFlood/100), st2.Skipped, ms(tIdle)))

	fmt.Printf("\n%s regflow: %d failed checks\n", server, c.fails)
	return c.fails
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000) }

func unwrapS3(err error) error {
	if err == nil {
		return nil
	}
	// dynamitedb flattens S3 errors to strings; map the status text back.
	if strings.Contains(err.Error(), "StatusCode: 403") || strings.Contains(err.Error(), "AccessDenied") {
		return fmt.Errorf("StatusCode: 403 %w", err)
	}
	return err
}

func forgeReg(victimSig ed25519.PublicKey, atk *reg.WorkerKeys, now time.Time) (string, []byte) {
	id := reg.WorkerID(victimSig, atk.Suite, atk.KEMPub)
	body := reg.MarshalBody(reg.RegBody{V: 1, WorkerID: id, SigPub: victimSig, Suite: uint16(atk.Suite), KEMPub: atk.KEMPub,
		Platform: "aarch64-linux", Created: now.Unix(), Nonce: make([]byte, 16)})
	return id, body
}

func clearFleet(ctx context.Context, c *s3.Client) {
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String("bob-fleet")})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return
		}
		for _, o := range out.Contents {
			c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("bob-fleet"), Key: o.Key})
		}
	}
}

var _ = io.EOF
