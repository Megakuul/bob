package worker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/megakuul/lake"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/isolation"
	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
)

// ProcessConfig configures the worker loop of `bob-server worker`.
type ProcessConfig struct {
	Fleet    *store.Bucket
	Name     string // BOB_SERVER_NAME (logs only; the worker id comes from its keys)
	StateDir string // worker state; the persistent store is <StateDir>/worker/nix
	Slots    int
	// Params completes the fleet/cfg.pr parameters (defaults, environment).
	Params func(store.FleetParams) store.FleetParams
	Now    func() time.Time
	Log    *slog.Logger
	// Executable is the bob-server binary bound into job containers ("" = this binary).
	Executable string
	// Substituters and TrustedKeys are bob's caches for the container's nix.conf.
	Substituters, TrustedKeys []string
	// Storage locates the analytics bucket of the job logs (lake); Orchestrators are the
	// Attach endpoints (BOB_ORCHESTRATOR_ADDRS; empty: the members of fleet/cfg.m).
	Storage       store.StorageConfig
	Orchestrators []string
	LogFlush      time.Duration // log.flush_interval; 0 = 30 s
	// Artifacts is the artifacts bucket (source snapshots, staging uploads); nil: jobs get no
	// source and stage nothing. Upstream is the public cache whose paths are not staged
	// (R-REG-005, "https://cache.nixos.org"; "" = stage every path).
	Artifacts Artifacts
	Upstream  string
}

// DefaultStateDir is the worker state directory when BOB_STATE_DIR is not set.
const DefaultStateDir = "/var/lib/bob"

// RegistrationWait is how long a worker waits for approval before it registers again with
// new keys (§05.6.3 worker side).
const RegistrationWait = 5 * time.Minute

// Process is the worker loop of a `bob-server worker` process: register, wait for approval,
// then claim and run jobs, each in its own job container.
type Process struct {
	cfg     ProcessConfig
	state   string // <StateDir>/worker
	store   string // persistent store, bound at /nix in every job container
	nixConf string
	exe     string
	slot    chan int // free slot numbers
	limits  isolation.Limits

	// Set once registration succeeded.
	id   *Identity
	logs *Logs // nil when the analytics bucket is unreachable: jobs run without live logs
	cur  atomic.Pointer[Worker]

	tokMu sync.Mutex
	tok   *Token
}

// NewProcess prepares the worker's state: the root-owned persistent store and the root-mode
// nix.conf of its job containers.
func NewProcess(cfg ProcessConfig) (*Process, error) {
	if cfg.Fleet == nil {
		return nil, errors.New("worker: needs the fleet bucket")
	}
	if cfg.Slots <= 0 {
		cfg.Slots = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Params == nil {
		cfg.Params = func(p store.FleetParams) store.FleetParams { return p }
	}
	base := cfg.StateDir
	if base == "" {
		base = DefaultStateDir
	}
	p := &Process{cfg: cfg, state: filepath.Join(base, "worker"), exe: cfg.Executable, slot: make(chan int, cfg.Slots)}
	p.store = filepath.Join(p.state, "nix")
	if err := os.MkdirAll(p.store, 0o755); err != nil {
		return nil, fmt.Errorf("worker: persistent store: %w", err)
	}
	// Root-owned and not writable by anyone else: builds run as other uids.
	if err := os.Chmod(p.state, 0o755); err != nil {
		return nil, fmt.Errorf("worker: %w", err)
	}
	p.nixConf = filepath.Join(p.state, "etc", "nix.conf")
	if err := os.MkdirAll(filepath.Dir(p.nixConf), 0o755); err != nil {
		return nil, fmt.Errorf("worker: %w", err)
	}
	if err := os.WriteFile(p.nixConf, []byte(isolation.NixConf(cfg.Substituters, cfg.TrustedKeys)), 0o644); err != nil {
		return nil, fmt.Errorf("worker: nix.conf: %w", err)
	}
	if p.exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("worker: own executable: %w", err)
		}
		p.exe = exe
	}
	for i := range cfg.Slots {
		p.slot <- i
	}
	p.limits = slotLimits(cfg.Slots)
	return p, nil
}

// slotLimits divides the host among the slots: memory and CPUs evenly, a fixed pids budget.
func slotLimits(slots int) isolation.Limits {
	l := isolation.Limits{Pids: 16384, CPUs: max(1, runtime.NumCPU()/slots)}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					l.MemoryBytes = kb << 10 / int64(slots)
				}
			}
		}
	}
	return l
}

// Platform is the Nix system of this worker.
func Platform() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64-linux"
	case "amd64":
		return "x86_64-linux"
	}
	return runtime.GOARCH + "-linux"
}

// Name implements server.Component.
func (p *Process) Name() string { return "worker loop" }

// Critical implements server.Critical: a revoked worker ends its process (a new process
// registers with new keys); everything transient is retried inside Run.
func (p *Process) Critical() bool { return true }

// Run registers, waits for approval and runs the job loop until ctx ends. It returns an error
// wrapping ErrRevoked when the worker is revoked.
func (p *Process) Run(ctx context.Context) error {
	log := p.cfg.Log
	id, tok, err := p.register(ctx)
	if err != nil || id == nil {
		return err
	}
	p.id, p.tok = id, tok
	log.Info("worker registered", "wid", id.ID(), "platform", id.Profile.Platform, "slots", p.cfg.Slots)
	bctx, stop := context.WithCancel(ctx)
	var bg []<-chan error
	defer func() {
		stop()
		for _, c := range bg {
			<-c
		}
	}()
	if logs, err := NewProcessLogs(ctx, LogConfig{Storage: p.cfg.Storage, Wid: id.ID(), FlushInterval: p.cfg.LogFlush, Now: p.cfg.Now, Log: log}); err != nil {
		log.Error("worker: job logs not available", "err", err.Error())
	} else {
		p.logs = logs
		// The lake writer outlives the job loop by its last flush (Run ends after ctx).
		bg = append(bg, safe.Go(func() error { return logs.Run(bctx) }))
	}
	bg = append(bg, safe.Go(func() error { return p.refreshToken(bctx) }))
	for _, addr := range p.attachAddrs(ctx) {
		bg = append(bg, safe.Go(func() error { return p.attachLoop(bctx, addr) }))
	}
	for {
		fc, err := p.fleetConfig(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Warn("worker: fleet/cfg unreadable; retrying", "err", err.Error())
			if !sleep(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		err = p.loop(ctx, id, fc)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrRevoked):
			return err
		case err != nil:
			log.Warn("worker loop ended; restarting", "err", err.Error())
			if !sleep(ctx, time.Second) {
				return nil
			}
		}
	}
}

// register creates a fresh identity, registers it and waits for the registrar's approval
// (worker/<wid>, polled every second). After RegistrationWait it starts over with new keys.
func (p *Process) register(ctx context.Context) (*Identity, *Token, error) {
	log := p.cfg.Log
	for {
		id, err := NewIdentity(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		id.Profile = store.WorkerProfile{Platform: Platform()}
		if _, err := Register(ctx, p.cfg.Fleet, id, p.cfg.Now()); err != nil {
			if ctx.Err() != nil {
				return nil, nil, nil
			}
			log.Warn("worker: registration failed; retrying", "err", err.Error())
			if !sleep(ctx, 2*time.Second) {
				return nil, nil, nil
			}
			continue
		}
		deadline := time.Now().Add(RegistrationWait)
		for time.Now().Before(deadline) {
			s, err := FetchSealed(ctx, p.cfg.Fleet, id.ID())
			switch {
			case err == nil:
				tok, err := id.OpenToken(s)
				if err != nil {
					return nil, nil, fmt.Errorf("worker %s: the sealed token does not open: %w", id.ID(), err)
				}
				return id, tok, nil
			case errors.Is(err, ErrRevoked):
				return nil, nil, err
			case !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil:
				log.Warn("worker: reading the worker record failed", "err", err.Error())
			}
			if !sleep(ctx, time.Second) {
				return nil, nil, nil
			}
		}
		log.Warn("worker: not approved within the registration wait; registering again with new keys", "wid", id.ID())
	}
}

func (p *Process) fleetConfig(ctx context.Context) (*store.FleetConfig, error) {
	fc, err := store.Get(ctx, p.cfg.Fleet, store.FleetConfigKey())
	if errors.Is(err, store.ErrNotFound) {
		return &store.FleetConfig{Name: "cfg"}, nil
	}
	return fc, err
}

func classIDs(fc *store.FleetConfig) []string {
	ids := make([]string, 0, len(fc.Classes))
	for _, c := range fc.Classes {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	return ids
}

// loop runs one Worker over the classes of fc until ctx ends, the worker is revoked, or the
// published classes change while it is idle (the Worker reads a fixed class set).
func (p *Process) loop(ctx context.Context, id *Identity, fc *store.FleetConfig) error {
	pr := p.cfg.Params(fc.Params)
	wc := Config{Fleet: p.cfg.Fleet, ID: id.ID(), Profile: id.Profile, Slots: p.cfg.Slots, SigKey: id.SigningKey(),
		PollInterval: pr.PollInterval, Heartbeat: pr.Heartbeat, LeaseTTL: pr.LeaseTTL, Skew: pr.Skew,
		Seed: uint64(time.Now().UnixNano()), Now: p.cfg.Now, Exec: p.exec}
	for _, k := range fc.Keys {
		if len(k.Pub) == ed25519.PublicKeySize {
			wc.Keys = append(wc.Keys, ed25519.PublicKey(k.Pub))
		}
	}
	for _, c := range fc.Classes {
		wc.Classes = append(wc.Classes, store.Class{Platform: c.Platform, Caps: c.Caps, Labels: c.Labels})
	}
	w, err := New(wc)
	if err != nil {
		return err
	}
	p.cur.Store(w)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	have := classIDs(fc)
	watch := safe.Go(func() error {
		var cache store.Cache[store.FleetConfig]
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return nil
			case <-t.C:
			}
			cur, _, err := store.GetCached(wctx, p.cfg.Fleet, &cache, store.FleetConfigKey())
			if err != nil || slices.Equal(classIDs(cur), have) || w.FreeSlots() < p.cfg.Slots {
				continue
			}
			cancel() // idle and the classes changed: restart over the new set
			return nil
		}
	})
	err = w.Run(wctx)
	cancel()
	<-watch
	return err
}

// exec runs one claimed job in a fresh job container (SPEC §05.8) and returns its outcome.
func (p *Process) exec(ctx context.Context, c Claim) Outcome {
	var slot int
	select {
	case slot = <-p.slot:
	case <-ctx.Done():
		return Outcome{Status: "infra", Error: "job cancelled before it started"}
	}
	defer func() { p.slot <- slot }()
	dir := filepath.Join(p.state, "jobs", c.JobID)
	_ = os.RemoveAll(dir) // a leftover of a worker that died in this job
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o755); err != nil {
		return Outcome{Status: "infra", Error: "job directory: " + err.Error()}
	}
	defer os.RemoveAll(dir)
	in, err := json.Marshal(Job{ID: c.JobID, Epoch: c.Epoch, Attempt: c.Attempt, Spec: c.Spec, Slot: slot})
	if err != nil {
		return Outcome{Status: "infra", Error: err.Error()}
	}
	errTail := &tail{max: 8 << 10}
	var jl *JobLog
	spec, _ := fleet.DecodeSpec(c.Spec)
	if p.logs != nil && spec.BuildID != "" {
		if jl, err = p.logs.Start(ctx, spec.BuildID, c.JobID, c.Epoch, int32(c.Attempt), nil); err != nil {
			p.cfg.Log.Warn("worker: job log not started", "job", c.JobID, "err", err.Error())
			jl = nil
		}
	}
	if p.cfg.Artifacts != nil {
		if err := p.prepareSource(ctx, c, spec, filepath.Join(dir, "work")); err != nil {
			var jf *jobFailed
			if errors.As(err, &jf) {
				return Outcome{Status: "failed", Error: jf.msg}
			}
			return Outcome{Status: "infra", Error: "job source: " + err.Error()}
		}
	}
	// The engine's state home (evaluator seed, drv cache) lives per slot across jobs: one job
	// at a time uses it, and rebuilding the seed for every job would cost minutes.
	home := filepath.Join(p.state, "home", strconv.Itoa(slot))
	if err := os.MkdirAll(home, 0o755); err != nil {
		return Outcome{Status: "infra", Error: "job home: " + err.Error()}
	}
	pr, pw := io.Pipe()
	var out Output
	if jl != nil {
		out = &jobLogOutput{jl: jl, build: spec.BuildID, project: spec.Project, worker: p.id.ID()}
	}
	var outcome *jobOutcome
	events := safe.Go(func() error {
		var err error
		outcome, err = readEvents(pr, out)
		_, _ = io.Copy(io.Discard, pr) // never block the container on a full pipe
		return err
	})
	exit, err := isolation.Run(ctx, isolation.Spec{
		JobID: c.JobID, RootDir: filepath.Join(dir, "root"), Store: p.store,
		Binds: []isolation.Bind{
			{Host: p.exe, Path: "/bin/bob"},
			{Host: p.nixConf, Path: "/etc/nix/nix.conf"},
			{Host: filepath.Join(dir, "work"), Path: "/work", Writable: true},
			{Host: home, Path: JobHome, Writable: true},
		},
		Args: []string{"/bin/bob", "job-run"},
		Env: []string{"PATH=/bin", "HOME=/root", "TMPDIR=/tmp", "NIX_CONF_DIR=/etc/nix",
			"NIX_SSL_CERT_FILE=" + isolation.CABundle, "SSL_CERT_FILE=" + isolation.CABundle},
		Dir: "/work", Limits: p.limits,
		Stdin: bytes.NewReader(in), Stdout: pw, Stderr: errTail,
	})
	_ = pw.Close()
	evErr := <-events
	var o Outcome
	switch {
	case err != nil:
		o = Outcome{Status: "infra", Error: fmt.Sprintf("job container: %v %s", err, errTail.String())}
	case exit != 0 || evErr != nil || outcome == nil || outcome.Status == "":
		o = Outcome{Status: "infra", Error: fmt.Sprintf("job container exited %d (%v): %s", exit, evErr, errTail.String())}
	default:
		o = Outcome{Status: outcome.Status, Error: outcome.Error}
	}
	if jl != nil {
		// Commit the job's log before the result names its last seqs (R-LOG-001 "at job end").
		// A commit that does not land in time stays queued in the writer.
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		last, err := jl.End(ectx)
		cancel()
		if err != nil {
			p.cfg.Log.Warn("worker: job log not committed at job end", "job", c.JobID, "err", err.Error())
		}
		o.LogLast = last
	}
	p.stage(ctx, c, spec, filepath.Join(dir, "work"), &o)
	return o
}

// jobLogOutput feeds a job container's output events into the job's log.
type jobLogOutput struct {
	jl                     *JobLog
	build, project, worker string
}

func (o *jobLogOutput) Line(pkg string, stream int32, text string) { o.jl.Line(pkg, stream, text) }

func (o *jobLogOutput) EndPackage(pkg string, e PackageEnd) {
	o.jl.EndPackage(pkg, &logstore.PackageRun{
		BuildID: lake.NewString(o.build), Project: lake.NewString(o.project), Package: lake.NewString(pkg),
		Drv: lake.NewString(e.Drv), Worker: lake.NewString(o.worker), Status: lake.NewString(e.Status),
		Start: lake.NewInt(e.Start.UnixNano()), End: lake.NewInt(e.End.UnixNano()),
		DurationMs: lake.NewInt(e.End.Sub(e.Start).Milliseconds()), Lines: lake.NewInt(e.Lines),
	})
}

// tail keeps the last max bytes written to it.
type tail struct {
	max int
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(b), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.buf)) }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
