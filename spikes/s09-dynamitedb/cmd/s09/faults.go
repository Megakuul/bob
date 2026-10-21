package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/fleet"
	"bobspike/s09/internal/model"
	"bobspike/s09/internal/store"
)

func isPUT(r *http.Request) bool { return r.Method == http.MethodPut }
func isPUTIfMatch(r *http.Request) bool {
	return r.Method == http.MethodPut && r.Header.Get("If-Match") != ""
}

func faults(ctx context.Context) error {
	cn := conn(ctx, "s09-faults", nil, 0, true)
	b := cn.Bucket
	out := func(name string, err error, extra string) {
		kind := "ok"
		switch {
		case errors.Is(err, store.ErrPanic):
			kind = "PANIC"
		case errors.Is(err, d.ErrConcurrencyConflict):
			kind = "ErrConcurrencyConflict"
		case errors.Is(err, d.ErrAlreadyExists):
			kind = "ErrAlreadyExists"
		case errors.Is(err, d.ErrNotFound):
			kind = "ErrNotFound"
		case err != nil:
			kind = "opaque error"
		}
		fmt.Printf("[%s] %-46s -> %-22s %s %s\n", *backend, name, kind, firstLine(err), extra)
	}
	k := func(s string) *T { return &T{P: d.Key("f"), S: d.Key(s)} }
	_ = store.Put(ctx, b, &T{P: d.Key("f"), S: d.Key("a"), N: d.Set(1)})

	cn.T.AddFault(&store.Fault{Mode: "500", Remaining: 2})
	_, err := store.Get(ctx, b, k("a"))
	out("2x HTTP 500 then success (SDK retries 3x)", err, "")
	cn.T.ClearFaults()

	cn.T.AddFault(&store.Fault{Mode: "500", Remaining: -1})
	t := time.Now()
	_, err = store.Get(ctx, b, k("a"))
	out("persistent HTTP 500", err, fmt.Sprintf("after %v", time.Since(t).Round(time.Millisecond)))
	cn.T.ClearFaults()

	cn.T.AddFault(&store.Fault{Mode: "503", Remaining: -1})
	err = store.Update(ctx, b, &T{P: d.Key("f"), S: d.Key("a"), N: d.Increment(1)})
	out("persistent 503 SlowDown on Update", err, "")
	cn.T.ClearFaults()

	cn.T.AddFault(&store.Fault{Mode: "reset", Remaining: -1})
	err = store.Put(ctx, b, &T{P: d.Key("f"), S: d.Key("a"), N: d.Set(1)})
	out("connection reset on every request", err, "")
	cn.T.ClearFaults()

	cn.T.AddFault(&store.Fault{Mode: "lostresp", Match: isPUT, Remaining: 1})
	err = store.Create(ctx, b, &T{P: d.Key("f"), S: d.Key("new1"), N: d.Set(1)})
	_, gerr := store.Get(ctx, b, k("new1"))
	out("Create: response lost, SDK retries", err, fmt.Sprintf("(object exists afterwards: %v)", gerr == nil))
	cn.T.ClearFaults()

	cn.T.AddFault(&store.Fault{Mode: "lostresp", Match: isPUTIfMatch, Remaining: 1})
	err = store.Update(ctx, b, &T{P: d.Key("f"), S: d.Key("a"), N: d.Set(42)})
	g, _ := store.Get(ctx, b, k("a"))
	out("Update: response lost, SDK retries", err, fmt.Sprintf("(value written: %v)", g != nil && g.N.Value() == 42))
	cn.T.ClearFaults()

	// claim with a lost PUT response: fleet.Claim must detect that it actually won
	q := fleet.New(cn)
	id := model.NewID(time.Now())
	_ = q.Enqueue(ctx, fleet.JobRef{Platform: "fault", ID: id, Build: "b"}, nil)
	cn.T.AddFault(&store.Fault{Mode: "lostresp", Match: isPUTIfMatch, Remaining: 1})
	cl, err := q.Claim(ctx, "w1", "fault", nil)
	out("Claim: PUT response lost", err, fmt.Sprintf("(claimed=%v ambiguousWins=%d)", cl != nil, q.AmbiguousWins))
	cn.T.ClearFaults()

	cn.T.AddFault(&store.Fault{Mode: "slow", Delay: 5 * time.Second, Remaining: -1})
	tctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	t = time.Now()
	_, err = store.Get(tctx, b, k("a"))
	cancel()
	out("slow backend, ctx deadline 500ms", err, fmt.Sprintf("after %v; errors.Is(DeadlineExceeded)=%v", time.Since(t).Round(time.Millisecond), errors.Is(err, context.DeadlineExceeded)))
	cn.T.ClearFaults()

	down := store.Open("http://127.0.0.1:20999", "s09-faults", *ak, *sk, nil, 0)
	t = time.Now()
	_, err = store.Get(ctx, down.Bucket, k("a"))
	out("S3 endpoint down (connection refused)", err, fmt.Sprintf("after %v", time.Since(t).Round(time.Millisecond)))
	return nil
}
