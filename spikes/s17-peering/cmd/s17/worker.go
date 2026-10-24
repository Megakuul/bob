package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// simWorker runs one job (one build, several packages) and emits log lines.
type simWorker struct {
	build, mode, tok string
	pkgs             []string
	rate             int // lines per second, whole build
	att              int32
	members          func() []Member
	cli              *s3.Client
	s3c              *s3Count
	pushBytes        *atomic.Int64

	mu        sync.Mutex
	seq       map[string]int64
	log       []Line
	committed int
	restarts  []Restart
	targets   map[string]int // orch id -> next index into log
	down      map[string]time.Time
	httpc     *http.Client
	clients   map[string]*connect.Client[PushReq, PushRes]
}

var insecureH2 = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 200}, Timeout: 5 * time.Second}

func newWorker(build, mode, tok string, npkg, rate int, members func() []Member, cli *s3.Client, s3c *s3Count, pb *atomic.Int64) *simWorker {
	w := &simWorker{build: build, mode: mode, tok: tok, rate: rate, att: 1, members: members, cli: cli, s3c: s3c, pushBytes: pb,
		seq: map[string]int64{}, targets: map[string]int{}, down: map[string]time.Time{}, httpc: insecureH2, clients: map[string]*connect.Client[PushReq, PushRes]{}}
	for i := 0; i < npkg; i++ {
		w.pkgs = append(w.pkgs, fmt.Sprintf("p%d", i))
	}
	return w
}

// resumeFromLake prepares attempt att: seq continues after the last persisted line.
func (w *simWorker) resumeFromLake(ctx context.Context, att int32) error {
	keys, err := listLake(ctx, w.cli, w.build)
	if err != nil {
		return err
	}
	w.att = att
	for _, k := range keys {
		parts := strings.Split(k, "/")
		if len(parts) != 4 {
			continue
		}
		var a int32
		var f, l int64
		if _, err := fmt.Sscanf(parts[3], "%03d-%010d-%010d.jsonl", &a, &f, &l); err == nil && l > w.seq[parts[2]] {
			w.seq[parts[2]] = l
		}
	}
	for _, p := range w.pkgs {
		w.restarts = append(w.restarts, Restart{p, att, w.seq[p] + 1})
	}
	return nil
}

// run produces lines for dur, pushing live every 200 ms and flushing to the lake every flush.
// crash=true stops without the final flush (lines since the last commit are lost).
func (w *simWorker) run(ctx context.Context, dur, flush time.Duration, crash bool) {
	gen := time.NewTicker(10 * time.Millisecond)
	push := time.NewTicker(200 * time.Millisecond)
	fl := time.NewTicker(flush)
	defer gen.Stop()
	defer push.Stop()
	defer fl.Stop()
	end := time.After(dur)
	acc := 0.0
	var pushWG sync.WaitGroup
	pushing := atomic.Bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-gen.C:
			acc += float64(w.rate) / 100
			w.mu.Lock()
			for ; acc >= 1; acc-- {
				p := w.pkgs[len(w.log)%len(w.pkgs)]
				w.seq[p]++
				w.log = append(w.log, Line{Pkg: p, Seq: w.seq[p], Att: w.att, Stream: 1, TS: time.Now().UnixNano(),
					Text: fmt.Sprintf("[%s] compiling unit %06d of %s: cc -O2 -c src/file_%06d.c -o obj/file_%06d.o", p, w.seq[p], w.build, w.seq[p], w.seq[p])})
			}
			w.mu.Unlock()
		case <-push.C:
			if pushing.CompareAndSwap(false, true) {
				pushWG.Add(1)
				go func() { defer pushWG.Done(); defer pushing.Store(false); w.pushOnce(ctx, nil) }()
			}
		case <-fl.C:
			w.flush(ctx)
		case <-end:
			pushWG.Wait()
			if crash {
				return
			}
			w.flush(ctx)
			w.mu.Lock()
			var fin []Final
			for _, p := range w.pkgs {
				fin = append(fin, Final{p, w.seq[p], w.att})
			}
			w.mu.Unlock()
			for i := 0; i < 5; i++ {
				if putEnd(ctx, w.cli, w.build, fin) == nil {
					break
				}
				time.Sleep(time.Second)
			}
			w.pushOnce(ctx, fin)
			return
		}
	}
}

func (w *simWorker) flush(ctx context.Context) {
	w.mu.Lock()
	lines := append([]Line(nil), w.log[w.committed:]...)
	n := len(w.log)
	w.mu.Unlock()
	by := map[string][]Line{}
	for _, l := range lines {
		by[l.Pkg] = append(by[l.Pkg], l)
	}
	for _, p := range w.pkgs {
		ls := by[p]
		if len(ls) == 0 {
			continue
		}
		if err := putJSONL(ctx, w.cli, segKey(w.build, p, w.att, ls[0].Seq, ls[len(ls)-1].Seq), ls); err != nil {
			return // retried with the next flush; committed stays
		}
	}
	w.mu.Lock()
	w.committed = n
	w.mu.Unlock()
}

func (w *simWorker) client(m Member) *connect.Client[PushReq, PushRes] {
	k := m.ID + m.Endpoint
	if c := w.clients[k]; c != nil {
		return c
	}
	c := connect.NewClient[PushReq, PushRes](w.httpc, m.Endpoint+procPush, connect.WithCodec(jsonCodec{}))
	w.clients[k] = c
	return c
}

func (w *simWorker) pushOnce(ctx context.Context, fin []Final) {
	if w.mode == "lakeonly" || w.mode == "lakeprivate" {
		return
	}
	ms := w.members()
	var tg []Member
	now := time.Now()
	if w.mode == "peer" {
		byID := map[string]Member{}
		var ids []string
		for _, m := range ms {
			byID[m.ID] = m
			ids = append(ids, m.ID)
		}
		for _, id := range rendezvous(w.build, ids) {
			if now.After(w.down[id]) {
				tg = append(tg, byID[id])
				break
			}
		}
	} else { // pushall
		for _, m := range ms {
			if now.After(w.down[m.ID]) {
				tg = append(tg, m)
			}
		}
	}
	w.mu.Lock()
	for id := range w.targets { // forget targets not chosen now: a later switch back replays from committed
		keep := false
		for _, m := range tg {
			keep = keep || m.ID == id
		}
		if !keep {
			delete(w.targets, id)
		}
	}
	w.mu.Unlock()
	var wg sync.WaitGroup
	for _, m := range tg {
		w.mu.Lock()
		next, ok := w.targets[m.ID]
		if !ok {
			next = w.committed
		}
		hi := min(len(w.log), next+5000)
		req := &PushReq{Build: w.build, Lines: append([]Line(nil), w.log[next:hi]...), Restarts: w.restarts}
		if hi == len(w.log) {
			req.End = fin
		}
		cl := w.client(m)
		w.mu.Unlock()
		if len(req.Lines) == 0 && req.End == nil && ok {
			continue
		}
		wg.Add(1)
		go func(m Member) {
			defer wg.Done()
			r := connect.NewRequest(req)
			r.Header().Set("Authorization", "Bearer "+w.tok)
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err := cl.CallUnary(c, r)
			w.mu.Lock()
			defer w.mu.Unlock()
			if err != nil {
				if os.Getenv("S17_DEBUG") != "" {
					fmt.Println("push error", m.ID, err)
				}
				w.down[m.ID] = time.Now().Add(5 * time.Second)
				delete(w.targets, m.ID)
				return
			}
			w.pushBytes.Add(size(req))
			w.targets[m.ID] = hi
		}(m)
	}
	wg.Wait()
}
