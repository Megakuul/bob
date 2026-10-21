package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
)

func (e *env) scSec(mem func() []Member) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms := mem()
	by := map[string]Member{}
	for _, m := range ms {
		by[m.ID] = m
	}
	_, evil, _ := ed25519.GenerateKey(rand.Reader)
	pinned := func(m Member) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLS(m.CertFP), ForceAttemptHTTP2: true}}
	}
	peer := func(target, pinFrom, tok string) error {
		cl := connect.NewClient[FollowReq, FollowMsg](pinned(by[pinFrom]), by[target].Endpoint+procPeerFollow, connect.WithCodec(jsonCodec{}))
		r := connect.NewRequest(&FollowReq{Build: "sec-x"})
		r.Header().Set("Authorization", "Bearer "+tok)
		c, cc := context.WithTimeout(ctx, 2*time.Second)
		defer cc()
		st, err := cl.CallServerStream(c, r)
		if err != nil {
			return err
		}
		defer st.Close()
		st.Receive()
		if st.Err() != nil && !strings.Contains(st.Err().Error(), "deadline") {
			return st.Err()
		}
		return nil // accepted (stream open until deadline)
	}
	push := func(tok string, req *PushReq) error {
		cl := connect.NewClient[PushReq, PushRes](insecureH2, by["B"].Endpoint+procPush, connect.WithCodec(jsonCodec{}))
		r := connect.NewRequest(req)
		r.Header().Set("Authorization", "Bearer "+tok)
		_, err := cl.CallUnary(ctx, r)
		return err
	}
	now := time.Now().Unix()
	wt := workerToken(e.priv, "w1", []string{"sec-own"})
	big := strings.Repeat("x", 5000)
	cases := []struct {
		name     string
		wantDeny bool
		run      func() error
	}{
		{"peer: valid token A->B", false, func() error { return peer("B", "B", peerToken(e.priv, "A", "B")) }},
		{"peer: worker token on peer endpoint", true, func() error { return peer("B", "B", wt) }},
		{"peer: token for audience A replayed to B", true, func() error { return peer("B", "B", peerToken(e.priv, "C", "A")) }},
		{"peer: expired token", true, func() error {
			return peer("B", "B", sign(e.priv, "bpt1.", "bob-peer-v1", peerClaims{"A", "B", now - 200, now - 100}))
		}},
		{"peer: issuer not a live orchestrator", true, func() error { return peer("B", "B", peerToken(e.priv, "Z", "B")) }},
		{"peer: token signed with another key", true, func() error { return peer("B", "B", peerToken(evil, "A", "B")) }},
		{"peer: peer token with worker domain", true, func() error {
			return peer("B", "B", sign(e.priv, "bpt1.", "bob-wtok-v1", peerClaims{"A", "B", now, now + 60}))
		}},
		{"tls: endpoint of B with A's pin (redirected address)", true, func() error { return peer("B", "A", peerToken(e.priv, "A", "B")) }},
		{"push: own build", false, func() error {
			return push(wt, &PushReq{Build: "sec-own", Lines: []Line{{Pkg: "p0", Seq: 1, Att: 1, Text: "hi"}}})
		}},
		{"push: build of another worker", true, func() error {
			return push(wt, &PushReq{Build: "sec-other", Lines: []Line{{Pkg: "p0", Seq: 1, Att: 1, Text: "hi"}}})
		}},
		{"push: peer token as worker token", true, func() error {
			return push(peerToken(e.priv, "A", "B"), &PushReq{Build: "sec-own"})
		}},
		{"push: line > 4 KiB", true, func() error {
			return push(wt, &PushReq{Build: "sec-own", Lines: []Line{{Pkg: "p0", Seq: 2, Att: 1, Text: big}}})
		}},
		{"push: package name with control chars", true, func() error {
			return push(wt, &PushReq{Build: "sec-own", Lines: []Line{{Pkg: "p0\x1b[2J", Seq: 2, Att: 1, Text: "x"}}})
		}},
		{"push: 6 MB body", true, func() error {
			ls := make([]Line, 1500)
			for i := range ls {
				ls[i] = Line{Pkg: "p0", Seq: int64(i + 10), Att: 1, Text: big[:4000]}
			}
			return push(wt, &PushReq{Build: "sec-own", Lines: ls})
		}},
	}
	fails := 0
	for _, c := range cases {
		err := c.run()
		denied := err != nil
		ok := denied == c.wantDeny
		if !ok {
			fails++
		}
		msg := "accepted"
		if err != nil {
			msg = err.Error()
			if len(msg) > 90 {
				msg = msg[:90]
			}
		}
		fmt.Printf("%-4s %-52s -> %s\n", map[bool]string{true: "ok", false: "FAIL"}[ok], c.name, msg)
	}
	// flood: 1 MiB/s budget, 4 MiB burst
	acc, rej := 0, 0
	t0 := time.Now()
	for i := 0; time.Since(t0) < 3*time.Second; i++ {
		ls := make([]Line, 250)
		for j := range ls {
			ls[j] = Line{Pkg: "p1", Seq: int64(i*250 + j + 1), Att: 1, Text: big[:3900]}
		}
		if push(wt, &PushReq{Build: "sec-own", Lines: ls}) == nil {
			acc++
		} else {
			rej++
		}
	}
	fmt.Printf("flood: 3 s of ~1 MB pushes: accepted=%d rejected=%d (expect ~4 burst + ~3 refill)\n", acc, rej)
	fmt.Printf("security cases failed: %d\n", fails)
	return nil
}
