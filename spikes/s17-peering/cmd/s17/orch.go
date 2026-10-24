package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	d "github.com/megakuul/dynamitedb"
)

type stats struct {
	PushIn, PeerOut, PeerIn, ClientOut atomic.Int64
	PushReq, PushRejected, PeerRejected atomic.Int64
	LakePolls                           atomic.Int64
}

type orch struct {
	id, mode string
	k        int // peers to subscribe per build
	cli      *s3.Client
	db       *d.Bucket
	s3c      s3Count
	st       stats
	priv     ed25519.PrivateKey
	pub      ed25519.PublicKey

	mu      sync.Mutex
	members []Member
	bufs    map[string]*hub
	peerCl  map[string]*http.Client
	limits  map[string]*bucketLimit
}

type hub struct {
	buf    *Buf
	cancel context.CancelFunc
	subs   map[string]context.CancelFunc
}

var pkgRe = regexp.MustCompile(`^[A-Za-z0-9@._:/+-]{1,200}$`)

func runOrch(args []string) error {
	fs := flag.NewFlagSet("orch", flag.ContinueOnError)
	id := fs.String("id", "A", "orchestrator id")
	port := fs.Int("port", 21701, "listen port")
	mode := fs.String("mode", "peer", "peer | pushall | lakeonly | lakeprivate")
	ep := fs.String("s3", "http://127.0.0.1:21750", "S3 endpoint")
	k := fs.Int("k", 2, "peers to subscribe per build")
	hb := fs.Duration("hb", time.Second, "heartbeat/list interval (prod proposal 10s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	o := &orch{id: *id, mode: *mode, k: *k, bufs: map[string]*hub{}, peerCl: map[string]*http.Client{}, limits: map[string]*bucketLimit{}}
	o.cli = newS3(*ep, &o.s3c)
	o.db = d.NewFromClient(o.cli, fleetBucket)
	ctx := context.Background()
	seed, err := getCtl(ctx, o.cli, "okey/k1")
	if err != nil {
		return fmt.Errorf("read orchestrator key: %w", err)
	}
	o.priv = ed25519.NewKeyFromSeed(seed)
	o.pub = o.priv.Public().(ed25519.PublicKey)

	cert, fp := selfCert()
	me := Member{ID: o.id, Endpoint: fmt.Sprintf("https://127.0.0.1:%d", *port), CertFP: fp, Peering: o.mode == "peer"}
	started := time.Now().UTC()
	go func() {
		for {
			func() {
				defer func() { recover() }()
				c, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if err := putOrch(c, o.db, me, started); err != nil {
					log.Printf("heartbeat: %v", err)
				}
				if ms, err := listMembers(c, o.cli, o.db, 4**hb); err == nil {
					o.mu.Lock()
					o.members = ms
					o.mu.Unlock()
				}
			}()
			time.Sleep(*hb)
		}
	}()
	go o.gcLoop()

	opt := connect.WithCodec(jsonCodec{})
	mux := http.NewServeMux()
	mux.Handle(procPush, connect.NewUnaryHandler(procPush, o.push, opt, connect.WithReadMaxBytes(4<<20)))
	mux.Handle(procPeerFollow, connect.NewServerStreamHandler(procPeerFollow, o.peerFollow, opt, connect.WithReadMaxBytes(64<<10)))
	mux.Handle(procFollow, connect.NewServerStreamHandler(procFollow, o.follow, opt, connect.WithReadMaxBytes(64<<10)))
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(o.snapshot()) })
	srv := &http.Server{Handler: recoverMW(mux), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	log.Printf("orch %s mode=%s listening %s fp=%s", o.id, o.mode, me.Endpoint, fp[:12])
	return srv.ServeTLS(ln, "", "")
}

func recoverMW(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil && v != http.ErrAbortHandler {
				log.Printf("panic: %v\n%s", v, debug.Stack())
			}
		}()
		h.ServeHTTP(w, r)
	})
}

func getCtl(ctx context.Context, cli *s3.Client, key string) ([]byte, error) {
	o, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(ctlBucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer o.Body.Close()
	b, err := io.ReadAll(io.LimitReader(o.Body, 4096))
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(b)))
}

func (o *orch) isMember(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, m := range o.members {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (o *orch) hubFor(key string) *hub {
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.bufs[key]
	if h == nil {
		h = &hub{buf: newBuf(50000), subs: map[string]context.CancelFunc{}}
		o.bufs[key] = h
	}
	return h
}

// ---------- worker -> orchestrator ----------

type bucketLimit struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

const workerBytesPerSec = 1 << 20 // [proposal] 1 MiB/s per worker, burst 4 MiB

func (o *orch) allow(wid string, n int) bool {
	o.mu.Lock()
	l := o.limits[wid]
	if l == nil {
		l = &bucketLimit{tokens: 4 << 20, last: time.Now()}
		o.limits[wid] = l
	}
	o.mu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens = min(4<<20, l.tokens+now.Sub(l.last).Seconds()*workerBytesPerSec)
	l.last = now
	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	return true
}

func validPush(r *PushReq) error {
	if len(r.Lines) > 5000 || len(r.Restarts) > 1000 || len(r.End) > 10000 {
		return errors.New("too many items")
	}
	for _, l := range r.Lines {
		if !pkgRe.MatchString(l.Pkg) || l.Seq < 1 || l.Att < 1 || l.Att > 100 || len(l.Text) > 4096 {
			return fmt.Errorf("invalid line %q/%d", l.Pkg, l.Seq)
		}
	}
	for _, x := range r.Restarts {
		if !pkgRe.MatchString(x.Pkg) || x.From < 1 || x.Att < 2 || x.Att > 100 {
			return errors.New("invalid restart")
		}
	}
	return nil
}

func (o *orch) push(ctx context.Context, req *connect.Request[PushReq]) (*connect.Response[PushRes], error) {
	if o.mode == "lakeonly" || o.mode == "lakeprivate" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("live log ingest disabled"))
	}
	m := req.Msg
	wid, err := checkWorker(o.pub, strings.TrimPrefix(req.Header().Get("Authorization"), "Bearer "), m.Build)
	if err != nil {
		o.st.PushRejected.Add(1)
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("worker token invalid: %w", err))
	}
	if err := validPush(m); err != nil {
		o.st.PushRejected.Add(1)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	n := size(m)
	if !o.allow(wid, int(n)) {
		o.st.PushRejected.Add(1)
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("live log rate limit; lines stay in lake"))
	}
	o.st.PushIn.Add(n)
	o.st.PushReq.Add(1)
	h := o.hubFor(m.Build)
	h.buf.add(m.Lines, m.Restarts, m.End, originWorker)
	return connect.NewResponse(&PushRes{Accepted: len(m.Lines)}), nil
}

// ---------- orchestrator -> orchestrator ----------

func (o *orch) peerFollow(ctx context.Context, req *connect.Request[FollowReq], st *connect.ServerStream[FollowMsg]) error {
	if o.mode != "peer" {
		return connect.NewError(connect.CodeUnimplemented, errors.New("peering disabled"))
	}
	if _, err := checkPeer(o.pub, strings.TrimPrefix(req.Header().Get("Authorization"), "Bearer "), o.id, o.isMember); err != nil {
		o.st.PeerRejected.Add(1)
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("peer token invalid: %w", err))
	}
	if !pkgRe.MatchString(req.Msg.Build) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid build id"))
	}
	if err := st.Send(&FollowMsg{}); err != nil { // flush headers: the subscriber knows it is connected
		return err
	}
	cur := req.Msg.Cursor
	h := o.hubFor(req.Msg.Build)
	h.buf.mu.Lock()
	h.buf.followers++
	h.buf.mu.Unlock()
	defer func() {
		h.buf.mu.Lock()
		h.buf.followers--
		h.buf.idleSince = time.Now()
		h.buf.mu.Unlock()
	}()
	var off int64
	nr := -1
	for {
		s := h.buf.read(off)
		off = s.next
		msg := &FollowMsg{}
		for _, e := range s.ents {
			c := cur[e.l.Pkg]
			if e.origin == originWorker && (e.l.Seq > c.Seq || e.l.Att > c.Att) {
				msg.Lines = append(msg.Lines, e.l)
			}
		}
		if len(s.restarts) != nr {
			msg.Restarts, nr = s.restarts, len(s.restarts)
		}
		msg.End = s.end
		if len(msg.Lines) > 0 || len(msg.Restarts) > 0 || msg.End != nil {
			o.st.PeerOut.Add(size(msg))
			if err := st.Send(msg); err != nil {
				return err
			}
		}
		if s.end != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.ch:
		case <-time.After(15 * time.Second): // keepalive
			if err := st.Send(&FollowMsg{}); err != nil {
				return err
			}
		}
	}
}

func (o *orch) peerClient(m Member) *http.Client {
	o.mu.Lock()
	defer o.mu.Unlock()
	k := m.ID + m.CertFP
	if c := o.peerCl[k]; c != nil {
		return c
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLS(m.CertFP), ForceAttemptHTTP2: true,
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	o.peerCl[k] = c
	return c
}

// ensureFeeds starts the per-build feed goroutines once: peer subscriptions and lake poller.
func (o *orch) ensureFeeds(key, build string, h *hub) {
	o.mu.Lock()
	if h.cancel != nil {
		o.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	o.mu.Unlock()
	go o.lakeLoop(ctx, build, h.buf)
	if o.mode == "peer" {
		go o.peerManager(ctx, build, h)
	}
}

func (o *orch) peerManager(ctx context.Context, build string, h *hub) {
	defer func() { recover() }()
	for {
		o.mu.Lock()
		var ids []string
		byID := map[string]Member{}
		for _, m := range o.members {
			if m.Peering {
				ids = append(ids, m.ID)
				byID[m.ID] = m
			}
		}
		o.mu.Unlock()
		want := map[string]bool{}
		for _, id := range rendezvous(build, ids) {
			if id != o.id && len(want) < o.k {
				want[id] = true
			}
		}
		for id := range want {
			if h.subs[id] == nil {
				c, cancel := context.WithCancel(ctx)
				h.subs[id] = cancel
				go o.subscribe(c, build, byID[id], h.buf)
			}
		}
		for id, cancel := range h.subs {
			if !want[id] {
				cancel()
				delete(h.subs, id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (o *orch) subscribe(ctx context.Context, build string, m Member, b *Buf) {
	defer func() { recover() }()
	cl := connect.NewClient[FollowReq, FollowMsg](o.peerClient(m), m.Endpoint+procPeerFollow, connect.WithCodec(jsonCodec{}))
	for ctx.Err() == nil {
		req := connect.NewRequest(&FollowReq{Build: build, Cursor: b.hwm()})
		req.Header().Set("Authorization", "Bearer "+peerToken(o.priv, o.id, m.ID))
		st, err := cl.CallServerStream(ctx, req)
		if err == nil {
			for st.Receive() {
				msg := st.Msg()
				o.st.PeerIn.Add(size(msg))
				b.add(msg.Lines, msg.Restarts, msg.End, originPeer)
				if msg.End != nil {
					st.Close()
					return
				}
			}
			st.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// lakeLoop reads the persisted log when the live path cannot be trusted:
// always in lake-only modes (every 3 s), otherwise when a follower reports a gap, or when no
// live line arrived for 6 s while the build has not ended.
func (o *orch) lakeLoop(ctx context.Context, build string, b *Buf) {
	defer func() { recover() }()
	var last time.Time
	for {
		b.mu.Lock()
		want, live, ended := b.wantLake, b.lastLive, b.end != nil
		b.wantLake = false
		b.mu.Unlock()
		lakeOnly := o.mode == "lakeonly" || o.mode == "lakeprivate"
		due := time.Since(last) >= 3*time.Second
		if (lakeOnly && due && !ended) || want || (!ended && due && time.Since(live) > 6*time.Second) {
			last = time.Now()
			o.pollLake(ctx, build, b)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (o *orch) pollLake(ctx context.Context, build string, b *Buf) {
	o.st.LakePolls.Add(1)
	keys, err := listLake(ctx, o.cli, build)
	if err != nil {
		return
	}
	minFirst := map[string]map[int32]int64{}
	for _, k := range keys {
		b.mu.Lock()
		seen := b.lakeKeys[k]
		b.mu.Unlock()
		if strings.HasSuffix(k, "/_end.json") {
			if seen {
				continue
			}
			if raw, err := getObj(ctx, o.cli, k); err == nil {
				var f []Final
				if json.Unmarshal(raw, &f) == nil {
					b.add(nil, nil, f, originLake)
					b.mu.Lock()
					b.lakeKeys[k] = true
					b.mu.Unlock()
				}
			}
			continue
		}
		pkg, att, first, ok := segMeta(k)
		if !ok {
			continue
		}
		if minFirst[pkg] == nil {
			minFirst[pkg] = map[int32]int64{}
		}
		if f, ok := minFirst[pkg][att]; !ok || first < f {
			minFirst[pkg][att] = first
		}
		if seen {
			continue
		}
		raw, err := getObj(ctx, o.cli, k)
		if err != nil {
			continue
		}
		b.add(parseJSONL(raw), nil, nil, originLake)
		b.mu.Lock()
		b.lakeKeys[k] = true
		b.mu.Unlock()
	}
	var rs []Restart
	for p, m := range minFirst {
		for a, f := range m {
			if a > 1 {
				rs = append(rs, Restart{p, a, f})
			}
		}
	}
	b.add(nil, rs, nil, originLake)
}

// ---------- client -> orchestrator ----------

func (o *orch) follow(ctx context.Context, req *connect.Request[FollowReq], st *connect.ServerStream[FollowMsg]) error {
	build := req.Msg.Build
	if !pkgRe.MatchString(build) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid build id"))
	}
	key := build
	if o.mode == "lakeprivate" { // baseline: every follower polls the lake on its own
		key = fmt.Sprintf("%s#%p", build, req)
	}
	h := o.hubFor(key)
	o.ensureFeeds(key, build, h)
	h.buf.mu.Lock()
	h.buf.followers++
	h.buf.mu.Unlock()
	defer func() {
		h.buf.mu.Lock()
		h.buf.followers--
		h.buf.idleSince = time.Now()
		h.buf.mu.Unlock()
	}()
	m := newMerger(req.Msg.Cursor)
	var off int64
	var endSeen time.Time
	for {
		s := h.buf.read(off)
		off = s.next
		if s.evicted {
			h.buf.mu.Lock()
			h.buf.wantLake = true
			h.buf.mu.Unlock()
		}
		for _, r := range s.restarts {
			m.restart(r)
		}
		for _, e := range s.ents {
			m.offer(e.l)
		}
		m.drain()
		done := m.done(s.end)
		if len(m.out) > 0 || len(m.outR) > 0 || done {
			msg := &FollowMsg{Lines: m.out, Restarts: m.outR}
			if done {
				msg.End, msg.Done = s.end, true
			}
			o.st.ClientOut.Add(size(msg))
			if err := st.Send(msg); err != nil {
				return err
			}
			m.out, m.outR = nil, nil
		}
		if done {
			return nil
		}
		if s.end != nil && endSeen.IsZero() {
			endSeen = time.Now()
		}
		if m.gapOlderThan(time.Second) || (!endSeen.IsZero() && time.Since(endSeen) > time.Second) {
			h.buf.mu.Lock()
			h.buf.wantLake = true
			h.buf.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.ch:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (o *orch) gcLoop() {
	for {
		time.Sleep(5 * time.Second)
		o.mu.Lock()
		for k, h := range o.bufs {
			h.buf.mu.Lock()
			idle := h.buf.followers == 0 && time.Since(h.buf.idleSince) > 30*time.Second &&
				(h.buf.end != nil || time.Since(h.buf.lastLive) > 10*time.Minute)
			h.buf.mu.Unlock()
			if idle {
				if h.cancel != nil {
					h.cancel()
				}
				delete(o.bufs, k)
			}
		}
		o.mu.Unlock()
	}
}

type orchStats struct {
	ID                                     string
	PushIn, PeerOut, PeerIn, ClientOut     int64
	PushReq, PushRejected, PeerRejected    int64
	LakePolls, S3Req, S3Get, S3Put, S3List int64
	CPUms                                  int64
	Bufs                                   int
}

func (o *orch) snapshot() orchStats {
	o.mu.Lock()
	n := len(o.bufs)
	o.mu.Unlock()
	return orchStats{o.id, o.st.PushIn.Load(), o.st.PeerOut.Load(), o.st.PeerIn.Load(), o.st.ClientOut.Load(),
		o.st.PushReq.Load(), o.st.PushRejected.Load(), o.st.PeerRejected.Load(), o.st.LakePolls.Load(),
		o.s3c.Req.Load(), o.s3c.GetBytes.Load(), o.s3c.PutBytes.Load(), o.s3c.List.Load(), cpuMs(), n}
}

func cpuMs() int64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
	var u, s int64
	fmt.Sscan(f[11], &u)
	fmt.Sscan(f[12], &s)
	return (u + s) * 10
}
