package fleetsrv

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Config configures the fleet side of one orchestrator.
type Config struct {
	Name           string // orchestrator id
	Control, Fleet *store.Bucket
	Params         store.FleetParams
	Now            func() time.Time
	Log            *slog.Logger
	// Forge mints per-job checkout tokens (the forge adapters, M17.1); nil until they exist:
	// GetCheckoutCredential then answers that checkout is not available.
	Forge orchestrator.ForgeMinter
}

// SecondWakeAfter is when a still queued job wakes a second worker (R-FLEET-012).
const SecondWakeAfter = 2 * time.Second

// Fleet is the fleet side of one orchestrator.
type Fleet struct {
	cfg        Config
	Keys       *Keys
	Registrar  *orchestrator.Registrar
	Reconciler *orchestrator.Reconciler
	Hub        *orchestrator.Hub
	Checkouts  *orchestrator.Checkouts
	jobs       *orchestrator.Queue // class-less view: job reads, cancel, finalise

	mu      sync.Mutex
	sigKeys map[string]ed25519.PublicKey // wid -> result signing key (worker/<wid>.sp)
}

// New loads (or creates and publishes) the orchestrator signing key and builds the
// controllers. It performs the key's I/O only.
func New(ctx context.Context, cfg Config) (*Fleet, error) {
	if cfg.Control == nil || cfg.Fleet == nil || cfg.Name == "" {
		return nil, errors.New("fleetsrv: needs the control and fleet buckets and the orchestrator name")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	cfg.Params = Merge(cfg.Params, DefaultParams())
	keys, err := EnsureKeys(ctx, cfg.Control, cfg.Fleet, cfg.Now(), cfg.Params)
	if err != nil {
		return nil, fmt.Errorf("orchestrator signing key: %w", err)
	}
	f := &Fleet{cfg: cfg, Keys: keys, sigKeys: map[string]ed25519.PublicKey{}}
	f.Registrar, err = orchestrator.NewRegistrar(orchestrator.RegistrarConfig{
		Control: cfg.Control, Fleet: cfg.Fleet, Name: cfg.Name, Key: keys.Key, Kid: keys.Kid,
		Heartbeat: cfg.Params.Heartbeat, Now: cfg.Now, VerifyKeys: keys.Verify, WorkerShards: cfg.Params.WorkerShards,
	})
	if err != nil {
		return nil, err
	}
	f.jobs = f.queue("")
	f.Hub = orchestrator.NewHub(f.jobs)
	f.Reconciler = &orchestrator.Reconciler{Fleet: cfg.Fleet, Shards: cfg.Params.JobShards, LeaseTTL: cfg.Params.LeaseTTL,
		Skew: cfg.Params.Skew, ReslotAge: cfg.Params.ReslotAfter, Now: cfg.Now, Name: cfg.Name, UseLease: true, Queue: f.jobs}
	forge := cfg.Forge
	if forge == nil {
		forge = noForge{}
	}
	f.Checkouts = &orchestrator.Checkouts{Control: cfg.Control, Queue: f.jobs, Forge: forge, Now: cfg.Now}
	return f, nil
}

// queue is the orchestrator side of class's queue, signing with the fleet key.
func (f *Fleet) queue(class string) *orchestrator.Queue {
	return &orchestrator.Queue{Fleet: f.cfg.Fleet, Control: f.cfg.Control, Class: class, Shards: f.cfg.Params.JobShards,
		Key: f.Keys.Key, Kid: f.Keys.Kid, WorkerKey: f.workerKey, Now: f.cfg.Now}
}

// workerKey returns the result signing key of worker wid (worker/<wid>.sp; keys never change
// for a wid, so a found key is kept).
func (f *Fleet) workerKey(wid string) ed25519.PublicKey {
	f.mu.Lock()
	k, ok := f.sigKeys[wid]
	f.mu.Unlock()
	if ok {
		return k
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec, err := store.Get(ctx, f.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)})
	if err != nil || len(rec.SigPub.Value()) != ed25519.PublicKeySize {
		return nil
	}
	f.mu.Lock()
	f.sigKeys[wid] = ed25519.PublicKey(rec.SigPub.Value())
	f.mu.Unlock()
	return rec.SigPub.Value()
}

// Enqueue enqueues job j on class's queue and wakes one idle attached worker (R-FLEET-012);
// the build controller calls it.
func (f *Fleet) Enqueue(ctx context.Context, class string, j orchestrator.Job) (slot uint64, woken []string, err error) {
	if err := f.addClass(ctx, class); err != nil {
		return 0, nil, err
	}
	d := &orchestrator.Dispatcher{Queue: f.queue(class), Workers: f.Hub.Workers(), SecondWakeAfter: SecondWakeAfter, Seed: uint64(f.cfg.Now().UnixNano())}
	return d.Enqueue(ctx, j)
}

// AddClass publishes a scheduling class in fleet/cfg.c so that workers that satisfy it read
// its queue (fleet/cfg holds at most 256 classes).
func (f *Fleet) AddClass(ctx context.Context, c store.Class) (string, error) {
	id, err := store.ClassID(c)
	if err != nil {
		return "", err
	}
	err = store.Change(ctx, f.cfg.Fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		if cur == nil {
			return nil, errors.New("fleet/cfg is missing")
		}
		for _, have := range cur.Classes.Value() {
			if have.ID == id {
				return nil, store.ErrNoChange
			}
		}
		if len(cur.Classes.Value()) >= 256 {
			return nil, fmt.Errorf("fleet/cfg already holds 256 scheduling classes (the limit)")
		}
		next := *cur
		next.Classes = store.Set(append(append([]store.FleetClass(nil), cur.Classes.Value()...),
			store.FleetClass{ID: id, Platform: c.Platform, Caps: c.Caps, Labels: c.Labels}))
		next.Gen = store.Set(next.Gen.Value() + 1)
		return &next, nil
	})
	return id, err
}

// addClass checks that class is published (Enqueue on an unpublished class would never be
// claimed).
func (f *Fleet) addClass(ctx context.Context, class string) error {
	cfg, err := store.Get(ctx, f.cfg.Fleet, store.FleetConfigKey())
	if err != nil {
		return fmt.Errorf("fleet/cfg: %w", err)
	}
	for _, c := range cfg.Classes.Value() {
		if c.ID == class {
			return nil
		}
	}
	return fmt.Errorf("scheduling class %s is not published in fleet/cfg (AddClass first)", class)
}

// Components are the fleet controllers of this orchestrator (SPEC §05.11), each leader-only
// under its lease ctl/<name>: registrar (2 s; token rotation every 60 s), job-reconciler
// (10 s), forge-sweeper (60 s).
func (f *Fleet) Components() []*orchestrator.Controller {
	name, skew, now := f.cfg.Name, f.cfg.Params.Skew, f.cfg.Now
	cs := []*orchestrator.Controller{
		orchestrator.RegistrarController(f.Registrar, f.cfg.Fleet, name, skew, now),
		orchestrator.ReconcilerController(f.Reconciler, name, skew, now),
		orchestrator.ForgeSweeperController(f.Checkouts, f.cfg.Fleet, name, skew, now),
	}
	for _, c := range cs {
		c.Log = f.cfg.Log
	}
	return cs
}

// noForge is the checkout minter until the forge adapters exist (M17.1).
type noForge struct{}

func (noForge) Mint(context.Context, string, time.Time) (string, orchestrator.Checkout, error) {
	return "", orchestrator.Checkout{}, diag.NotImplemented("forge checkout credentials")
}

func (noForge) Revoke(context.Context, string) error {
	return diag.NotImplemented("forge checkout credentials")
}
