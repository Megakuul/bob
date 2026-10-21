package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	d "github.com/megakuul/dynamitedb"
)

var orchIDs = []string{"A", "B", "C"}

func port(id string) int { return 21701 + int(id[0]-'A') }

type env struct {
	ep    string
	cli   *s3.Client
	s3c   s3Count
	db    *d.Bucket
	priv  ed25519.PrivateKey
	procs map[string]*exec.Cmd
	mu    sync.Mutex
	mem   []Member
	stop  chan struct{}
}

func newEnv(ep string) (*env, error) {
	e := &env{ep: ep, procs: map[string]*exec.Cmd{}}
	e.cli = newS3(ep, &e.s3c)
	e.db = d.NewFromClient(e.cli, fleetBucket)
	ctx := context.Background()
	if err := ensureBuckets(ctx, e.cli); err != nil {
		return nil, err
	}
	seed := make([]byte, 32)
	rand.Read(seed)
	e.priv = ed25519.NewKeyFromSeed(seed)
	_, err := e.cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(ctlBucket), Key: aws.String("okey/k1"), Body: strings.NewReader(hex.EncodeToString(seed))})
	return e, err
}

func (e *env) wipe(prefix, bucket string) {
	ctx := context.Background()
	p := s3.NewListObjectsV2Paginator(e.cli, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		pg, err := p.NextPage(ctx)
		if err != nil {
			return
		}
		var ids []s3types.ObjectIdentifier
		for _, o := range pg.Contents {
			ids = append(ids, s3types.ObjectIdentifier{Key: o.Key})
		}
		for _, id := range ids {
			e.cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: id.Key})
		}
	}
}

func (e *env) start(mode string, ids ...string) error {
	for _, id := range ids {
		lf, _ := os.Create(fmt.Sprintf("tmp/orch-%s.log", id))
		c := exec.Command(os.Args[0], "orch", "-id", id, "-port", fmt.Sprint(port(id)), "-mode", mode, "-s3", e.ep)
		c.Stdout, c.Stderr = lf, lf
		c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} // never outlive the bench
		if err := c.Start(); err != nil {
			return err
		}
		e.procs[id] = c
	}
	for i := 0; i < 100; i++ {
		ms := e.members()
		have := 0
		for _, m := range ms {
			for _, id := range ids {
				if m.ID == id && m.Heartbeat.After(time.Now().Add(-2*time.Second)) {
					have++
				}
			}
		}
		if have == len(ids) {
			time.Sleep(1500 * time.Millisecond) // let every orchestrator see the others
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("orchestrators did not register")
}

func (e *env) kill(id string) {
	if c := e.procs[id]; c != nil {
		c.Process.Signal(syscall.SIGKILL)
		c.Wait()
		delete(e.procs, id)
	}
}

func (e *env) killAll() {
	for id := range e.procs {
		e.kill(id)
	}
	e.wipe("orch/", fleetBucket)
}

// member view for workers: refreshed every second from the fleet bucket.
func (e *env) members() []Member {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ms, _ := listMembers(ctx, e.cli, e.db, 4*time.Second)
	return ms
}

func (e *env) memberLoop() func() []Member {
	e.stop = make(chan struct{})
	e.mu.Lock()
	e.mem = e.members()
	e.mu.Unlock()
	go func() {
		for {
			select {
			case <-e.stop:
				return
			case <-time.After(time.Second):
			}
			ms := e.members()
			e.mu.Lock()
			e.mem = ms
			e.mu.Unlock()
		}
	}()
	return func() []Member { e.mu.Lock(); defer e.mu.Unlock(); return e.mem }
}

func (e *env) stats() map[string]orchStats {
	out := map[string]orchStats{}
	for id := range e.procs {
		r, err := insecureH2.Get(fmt.Sprintf("https://127.0.0.1:%d/stats", port(id)))
		if err != nil {
			continue
		}
		var s orchStats
		json.NewDecoder(r.Body).Decode(&s)
		r.Body.Close()
		out[id] = s
	}
	return out
}

// ---------- follower (UI/CLI client) ----------

type follower struct {
	build     string
	orchs     []string // connect order; on error the next one, with the cursor
	since     int64    // count latency only for lines produced after this (unix ns)
	lines     map[string][]Line
	gaps, dup int
	restarts  int
	reconn    int
	lat       []float64 // ms
	done      bool
	maxStall  time.Duration
	bytes     int64
}

func (f *follower) cursor() map[string]Cursor {
	c := map[string]Cursor{}
	for p, ls := range f.lines {
		if len(ls) > 0 {
			c[p] = Cursor{ls[len(ls)-1].Seq, ls[len(ls)-1].Att}
		}
	}
	return c
}

func (f *follower) run(ctx context.Context) {
	f.lines = map[string][]Line{}
	last := time.Now()
	for i := 0; ctx.Err() == nil && !f.done; i++ {
		id := f.orchs[i%len(f.orchs)]
		if i > 0 {
			f.reconn++
			time.Sleep(300 * time.Millisecond)
		}
		cl := connect.NewClient[FollowReq, FollowMsg](insecureH2NoTimeout, fmt.Sprintf("https://127.0.0.1:%d%s", port(id), procFollow), connect.WithCodec(jsonCodec{}))
		st, err := cl.CallServerStream(ctx, connect.NewRequest(&FollowReq{Build: f.build, Cursor: f.cursor()}))
		if err != nil {
			continue
		}
		for st.Receive() {
			m := st.Msg()
			now := time.Now()
			if g := now.Sub(last); g > f.maxStall {
				f.maxStall = g
			}
			last = now
			f.bytes += size(m)
			for _, r := range m.Restarts {
				f.restarts++
				ls := f.lines[r.Pkg]
				k := sort.Search(len(ls), func(i int) bool { return ls[i].Seq >= r.From })
				f.lines[r.Pkg] = ls[:k]
			}
			for _, l := range m.Lines {
				ls := f.lines[l.Pkg]
				var prev int64
				if len(ls) > 0 {
					prev = ls[len(ls)-1].Seq
				}
				switch {
				case l.Seq == prev+1:
				case l.Seq <= prev:
					f.dup++
					continue
				default:
					f.gaps++
				}
				f.lines[l.Pkg] = append(ls, l)
				if l.TS > f.since {
					f.lat = append(f.lat, float64(now.UnixNano()-l.TS)/1e6)
				}
			}
			if m.Done {
				f.done = true
			}
		}
		st.Close()
	}
}

var insecureH2NoTimeout = &(*insecureH2)

func init() {
	c := *insecureH2
	c.Timeout = 0
	insecureH2NoTimeout = &c
}

// truth reads the persisted log and applies the validity rule.
func (e *env) truth(build string) (map[string][]Line, error) {
	ctx := context.Background()
	keys, err := listLake(ctx, e.cli, build)
	if err != nil {
		return nil, err
	}
	m := newMerger(nil)
	minFirst := map[string]map[int32]int64{}
	var all []Line
	for _, k := range keys {
		if strings.HasSuffix(k, "_end.json") {
			continue
		}
		p, a, f, _ := segMeta(k)
		if minFirst[p] == nil {
			minFirst[p] = map[int32]int64{}
		}
		if v, ok := minFirst[p][a]; !ok || f < v {
			minFirst[p][a] = f
		}
		raw, err := getObj(ctx, e.cli, k)
		if err != nil {
			return nil, err
		}
		all = append(all, parseJSONL(raw)...)
	}
	for p, x := range minFirst {
		for a, f := range x {
			if a > 1 {
				m.restart(Restart{p, a, f})
			}
		}
	}
	for _, l := range all {
		m.offer(l)
	}
	m.drain()
	out := map[string][]Line{}
	for _, l := range m.out {
		out[l.Pkg] = append(out[l.Pkg], l)
	}
	return out, nil
}

func compare(got, want map[string][]Line) string {
	bad := 0
	n := 0
	for p, w := range want {
		g := got[p]
		n += len(w)
		if len(g) != len(w) {
			bad++
			continue
		}
		for i := range w {
			if g[i].Seq != w[i].Seq || g[i].Att != w[i].Att || g[i].Text != w[i].Text {
				bad++
				break
			}
		}
	}
	if bad == 0 && len(got) == len(want) {
		return fmt.Sprintf("EQUAL (%d lines)", n)
	}
	return fmt.Sprintf("DIFFERENT in %d packages", bad)
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return -1
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[min(len(s)-1, int(p*float64(len(s))))]
}

// pickBuild returns a build id whose rendezvous home is home.
func pickBuild(tag, home string) string {
	for i := 0; ; i++ {
		b := fmt.Sprintf("%s-%s%d", tag, time.Now().Format("150405"), i)
		if rendezvous(b, orchIDs)[0] == home {
			return b
		}
	}
}

// ---------- scenarios ----------

func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	ep := fs.String("s3", "http://127.0.0.1:21750", "S3 endpoint")
	sc := fs.String("scenario", "kill", "kill | killclient | retry | fanout | sec")
	mode := fs.String("mode", "peer", "peer | pushall | lakeonly | lakeprivate")
	dur := fs.Duration("dur", 30*time.Second, "build duration")
	builds := fs.Int("builds", 20, "fanout: concurrent builds")
	nf := fs.Int("followers", 50, "fanout: followers of build 0")
	rate := fs.Int("rate", 200, "lines/s per build")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv(*ep)
	if err != nil {
		return err
	}
	e.wipe("orch/", fleetBucket)
	defer e.killAll()
	if err := e.start(*mode, orchIDs...); err != nil {
		return err
	}
	mem := e.memberLoop()
	defer close(e.stop)
	switch *sc {
	case "kill", "killclient":
		return e.scKill(*sc, *mode, *dur, *rate, mem)
	case "retry":
		return e.scRetry(*mode, *rate, mem)
	case "fanout":
		return e.scFanout(*mode, *dur, *builds, *nf, *rate, mem)
	case "sec":
		return e.scSec(mem)
	}
	return fmt.Errorf("unknown scenario %s", *sc)
}

func (e *env) scKill(sc, mode string, dur time.Duration, rate int, mem func() []Member) error {
	ctx := context.Background()
	build := pickBuild("k", "B") // workers push to B
	var pb atomic.Int64
	w := newWorker(build, mode, workerToken(e.priv, "w1", []string{build}), 3, rate, mem, e.cli, &e.s3c, &pb)
	victim, order := "B", []string{"A", "C"}
	if sc == "killclient" {
		victim = "A" // the follower's own orchestrator dies; it reconnects to C with its cursor
	}
	if os.Getenv("S17_NOKILL") != "" {
		victim = "none"
	}
	f := &follower{build: build, orchs: order, since: time.Now().UnixNano()}
	fctx, cancel := context.WithTimeout(ctx, dur+40*time.Second)
	defer cancel()
	fdone := make(chan struct{})
	go func() { f.run(fctx); close(fdone) }()
	go func() { time.Sleep(dur / 3); e.kill(victim); fmt.Printf("t=%v killed %s\n", dur/3, victim) }()
	t0 := time.Now()
	w.run(ctx, dur, 5*time.Second, false)
	<-fdone
	want, err := e.truth(build)
	if err != nil {
		return err
	}
	fmt.Printf("scenario=%s mode=%s build=%s home=B killed=%s\n", sc, mode, build, victim)
	fmt.Printf("  follower: done=%v gaps=%d dups=%d restarts=%d reconnects=%d max_stall=%v finish_after_end=%v\n",
		f.done, f.gaps, f.dup, f.restarts, f.reconn, f.maxStall.Round(time.Millisecond), time.Since(t0)-dur)
	fmt.Printf("  vs lake truth: %s\n", compare(f.lines, want))
	fmt.Printf("  latency ms p50=%.0f p99=%.0f max=%.0f (n=%d)\n", pct(f.lat, .5), pct(f.lat, .99), pct(f.lat, 1), len(f.lat))
	for id, s := range e.stats() {
		fmt.Printf("  %s: push_in=%dB peer_in=%dB peer_out=%dB lake_polls=%d s3_get=%dB\n", id, s.PushIn, s.PeerIn, s.PeerOut, s.LakePolls, s.S3Get)
	}
	return nil
}

func (e *env) scRetry(mode string, rate int, mem func() []Member) error {
	ctx := context.Background()
	build := pickBuild("r", "B")
	var pb atomic.Int64
	tok := workerToken(e.priv, "w1", []string{build})
	f := &follower{build: build, orchs: []string{"A", "C"}, since: time.Now().UnixNano()}
	fctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	fdone := make(chan struct{})
	go func() { f.run(fctx); close(fdone) }()
	w1 := newWorker(build, mode, tok, 3, rate, mem, e.cli, &e.s3c, &pb)
	w1.run(ctx, 12*time.Second, 5*time.Second, true) // crash at 12 s: lines after the 10 s flush are lost
	lost := len(w1.log) - w1.committed
	time.Sleep(2 * time.Second)
	w2 := newWorker(build, mode, tok, 3, rate, mem, e.cli, &e.s3c, &pb)
	if err := w2.resumeFromLake(ctx, 2); err != nil {
		return err
	}
	w2.run(ctx, 10*time.Second, 5*time.Second, false)
	<-fdone
	want, err := e.truth(build)
	if err != nil {
		return err
	}
	fmt.Printf("scenario=retry mode=%s build=%s lost_unflushed_lines=%d restarts=%v\n", mode, build, lost, w2.restarts)
	fmt.Printf("  follower: done=%v gaps=%d dups=%d restart_markers=%d reconnects=%d\n", f.done, f.gaps, f.dup, f.restarts, f.reconn)
	fmt.Printf("  vs lake truth: %s\n", compare(f.lines, want))
	return nil
}

func (e *env) scFanout(mode string, dur time.Duration, nb, nf, rate int, mem func() []Member) error {
	ctx := context.Background()
	var pb atomic.Int64
	var bs []string
	for i := 0; i < nb; i++ {
		bs = append(bs, fmt.Sprintf("f-%s-%02d", time.Now().Format("150405"), i))
	}
	var fs []*follower
	start := time.Now().UnixNano()
	for i := 0; i < nf; i++ {
		fs = append(fs, &follower{build: bs[0], orchs: []string{orchIDs[i%3]}, since: start})
	}
	for i := 1; i < nb; i++ {
		fs = append(fs, &follower{build: bs[i], orchs: []string{orchIDs[i%3]}, since: start})
	}
	e.s3c.Req.Store(0); e.s3c.PutBytes.Store(0); e.s3c.GetBytes.Store(0)
	st0 := e.stats()
	fctx, cancel := context.WithTimeout(ctx, dur+40*time.Second)
	defer cancel()
	var fwg, wwg sync.WaitGroup
	for _, f := range fs {
		fwg.Add(1)
		go func() { defer fwg.Done(); f.run(fctx) }()
	}
	time.Sleep(500 * time.Millisecond)
	for _, b := range bs {
		wwg.Add(1)
		w := newWorker(b, mode, workerToken(e.priv, "w-"+b, []string{b}), 5, rate, mem, e.cli, &e.s3c, &pb)
		go func() { defer wwg.Done(); w.run(ctx, dur, 5*time.Second, false) }()
	}
	wwg.Wait()
	fwg.Wait()
	st1 := e.stats()
	var lat0, latAll []float64
	var gaps, dups, notDone int
	var clientBytes int64
	for i, f := range fs {
		if i < nf {
			lat0 = append(lat0, f.lat...)
		}
		latAll = append(latAll, f.lat...)
		gaps += f.gaps
		dups += f.dup
		clientBytes += f.bytes
		if !f.done {
			notDone++
		}
	}
	bad := 0
	for _, b := range bs[:min(3, nb)] {
		want, _ := e.truth(b)
		for _, f := range fs {
			if f.build == b && !strings.HasPrefix(compare(f.lines, want), "EQUAL") {
				bad++
			}
		}
	}
	var o orchStats
	for id, s := range st1 {
		p := st0[id]
		o.PushIn += s.PushIn - p.PushIn
		o.PeerOut += s.PeerOut - p.PeerOut
		o.ClientOut += s.ClientOut - p.ClientOut
		o.S3Get += s.S3Get - p.S3Get
		o.S3Req += s.S3Req - p.S3Req
		o.S3List += s.S3List - p.S3List
		o.LakePolls += s.LakePolls - p.LakePolls
		o.CPUms += s.CPUms - p.CPUms
	}
	secs := dur.Seconds()
	fmt.Printf("scenario=fanout mode=%s builds=%d followers(build0)=%d other_followers=%d rate=%d lines/s/build dur=%v\n", mode, nb, nf, nb-1, rate, dur)
	fmt.Printf("  correctness: gaps=%d dups=%d not_done=%d mismatches_vs_lake(first 3 builds)=%d\n", gaps, dups, notDone, bad)
	fmt.Printf("  latency build0 ms p50=%.0f p99=%.0f max=%.0f | all builds p50=%.0f p99=%.0f (n=%d)\n",
		pct(lat0, .5), pct(lat0, .99), pct(lat0, 1), pct(latAll, .5), pct(latAll, .99), len(latAll))
	fmt.Printf("  bytes (JSON payload): worker->orch %.1f MB, orch->orch %.1f MB, orch->clients %.1f MB\n",
		float64(pb.Load())/1e6, float64(o.PeerOut)/1e6, float64(o.ClientOut)/1e6)
	fmt.Printf("  S3 by orchestrators: %d req (%.1f req/s), %d LIST, GET %.1f MB, lake polls %d | S3 by workers: %d req, PUT %.1f MB\n",
		o.S3Req, float64(o.S3Req)/secs, o.S3List, float64(o.S3Get)/1e6, o.LakePolls, e.s3c.Req.Load(), float64(e.s3c.PutBytes.Load())/1e6)
	fmt.Printf("  orchestrator CPU total %.1f s (%.0f%% of one core)\n", float64(o.CPUms)/1000, float64(o.CPUms)/10/secs)
	return nil
}
