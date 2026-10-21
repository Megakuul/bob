package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/store"
)

type T struct {
	P     d.KeyField                `pk:"t" cbor:"-"`
	S     d.KeyField                `sk:"s" cbor:"-"`
	ETag  d.ETagField               `etag:"true" cbor:"-"`
	N     d.DataField[int]          `cbor:"n,omitempty"`
	Dur   d.DataField[time.Duration] `cbor:"d,omitempty"`
	Str   d.DataField[string]       `cbor:"str,omitempty"`
	Static string                   `cbor:"static,omitempty"`
}

type res struct{ name, result string; ok bool }

var results []res

func check(name string, ok bool, format string, a ...any) {
	results = append(results, res{name, fmt.Sprintf(format, a...), ok})
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	fmt.Printf("[%s] %-4s %-44s %s\n", *backend, mark, name, fmt.Sprintf(format, a...))
}

func probe(ctx context.Context) error {
	c := &store.Counter{}
	cn := conn(ctx, "s09-probe", c, 0, true)
	b, cli := cn.Bucket, cn.S3
	k := func(s string) *T { return &T{P: d.Key("p"), S: d.Key(s)} }

	// 1 Create twice
	err1 := store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key("c1"), N: d.Set(1)})
	err2 := store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key("c1"), N: d.Set(2)})
	check("create-if-none-match", err1 == nil && errors.Is(err2, d.ErrAlreadyExists), "first=%v second=%v", err1, err2)

	// 2 stale ETag on Put / Update / Delete
	got, _ := store.Get(ctx, b, k("c1"))
	stale := got.ETag
	_ = store.Update(ctx, b, &T{P: d.Key("p"), S: d.Key("c1"), N: d.Increment(1)})
	e := store.Put(ctx, b, &T{P: d.Key("p"), S: d.Key("c1"), ETag: stale, N: d.Set(9)})
	check("put-stale-etag-rejected", errors.Is(e, d.ErrConcurrencyConflict), "err=%v", e)
	e = store.Update(ctx, b, &T{P: d.Key("p"), S: d.Key("c1"), ETag: stale, N: d.Set(9)})
	check("update-stale-etag-rejected", errors.Is(e, d.ErrConcurrencyConflict), "err=%v", e)
	e = store.Delete(ctx, b, &T{P: d.Key("p"), S: d.Key("c1"), ETag: stale})
	check("delete-stale-etag-rejected", errors.Is(e, d.ErrConcurrencyConflict), "err=%v", e)

	// raw: does the backend honour If-Match on GET and DELETE itself?
	cur, _ := store.Get(ctx, b, k("c1"))
	_, gerr := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(cn.Name), Key: aws.String("t/p/s/c1"), IfMatch: aws.String(`"deadbeef"`)})
	check("backend-get-if-match", gerr != nil && strings.Contains(gerr.Error(), "412"), "err=%v", short(gerr))
	_, derr := cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cn.Name), Key: aws.String("t/p/s/c1"), IfMatch: aws.String(`"deadbeef"`)})
	_, still := store.Get(ctx, b, k("c1"))
	check("backend-delete-if-match", derr != nil && still == nil, "err=%v objectStillThere=%v", short(derr), still == nil)
	_ = cur

	// 3 raw concurrent PUT If-Match race: N writers with the same etag, exactly one may win
	for _, n := range []int{20} {
		wins := map[int]int{}
		rounds := 100
		key := "t/p/s/race"
		for r := 0; r < rounds; r++ {
			po, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cn.Name), Key: aws.String(key), Body: bytes.NewReader([]byte("x"))})
			if err != nil {
				return err
			}
			var w atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cn.Name), Key: aws.String(key),
						Body: bytes.NewReader([]byte(fmt.Sprintf("w%d", i))), IfMatch: po.ETag})
					if err == nil {
						w.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			wins[int(w.Load())]++
		}
		check("raw-put-if-match-race-20", len(wins) == 1 && wins[1] == rounds, "rounds=%d histogram(winners->rounds)=%v", rounds, wins)
	}
	// same for If-None-Match (Create)
	{
		wins := map[int]int{}
		rounds := 100
		for r := 0; r < rounds; r++ {
			key := fmt.Sprintf("t/p/s/inm%d", r)
			var w atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cn.Name), Key: aws.String(key),
						Body: bytes.NewReader([]byte("x")), IfNoneMatch: aws.String("*")})
					if err == nil {
						w.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			wins[int(w.Load())]++
		}
		check("raw-put-if-none-match-race-20", len(wins) == 1 && wins[1] == rounds, "rounds=%d histogram=%v", rounds, wins)
	}
	// raw DELETE If-Match race: N deleters with same etag -> at most one success
	{
		wins := map[int]int{}
		for r := 0; r < 50; r++ {
			key := "t/p/s/delrace"
			po, _ := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cn.Name), Key: aws.String(key), Body: bytes.NewReader([]byte("x"))})
			var w atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if _, err := cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cn.Name), Key: aws.String(key), IfMatch: po.ETag}); err == nil {
						w.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			wins[int(w.Load())]++
		}
		check("raw-delete-if-match-race-20", true, "rounds=50 histogram(successful deletes->rounds)=%v (info: >1 means deletes are not exclusive)", wins)
	}

	// 4 WithExpires
	_ = store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key("exp"), N: d.Set(1)}, d.WithExpires(time.Now().Add(time.Second)))
	time.Sleep(3 * time.Second)
	_, eerr := store.Get(ctx, b, k("exp"))
	check("with-expires-deletes-object", eerr != nil, "Get after expiry: err=%v (nil = object still readable)", eerr)

	// 5 static fields survive Create
	_ = store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key("static"), Static: "hello", N: d.Set(1)})
	sg, _ := store.Get(ctx, b, k("static"))
	check("static-field-stored-on-create", sg != nil && sg.Static == "hello", "static=%q (README: allowed but immutable)", func() string {
		if sg == nil {
			return "<nil>"
		}
		return sg.Static
	}())

	// 6 filter operators
	_ = store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key("f"), N: d.Set(5), Dur: d.Set(time.Hour)})
	_, e1 := store.Get(ctx, b, &T{P: d.Key("p"), S: d.Key("f"), N: d.LessOrEqThan(5)})
	check("filter-LessOrEqThan-int", e1 == nil, "N=5 LessOrEqThan(5): err=%v", e1)
	_, e2 := store.Get(ctx, b, &T{P: d.Key("p"), S: d.Key("f"), N: d.LessThan(6)})
	check("filter-LessThan-int", e2 == nil, "N=5 LessThan(6): err=%v", e2)
	_, e3 := store.Get(ctx, b, &T{P: d.Key("p"), S: d.Key("f"), Dur: d.GreaterThan(time.Minute)})
	check("filter-GreaterThan-duration", e3 == nil, "Dur=1h GreaterThan(1m): err=%v", e3)
	_, e4 := store.Get(ctx, b, &T{P: d.Key("p"), S: d.Key("f"), Dur: d.LessOrEqThan(time.Hour)})
	check("filter-LessOrEqThan-duration", e4 == nil, "Dur=1h LessOrEqThan(1h): err=%v", e4)

	// 7 Update returns new state / etag?
	um := &T{P: d.Key("p"), S: d.Key("f"), N: d.Increment(1)}
	_ = store.Update(ctx, b, um)
	_, isData := um.N.(interface{ Value() int })
	check("update-writes-back-model", um.ETag != nil, "after Update: model.ETag=%v (doc: 'Update will modify v')", um.ETag)
	_ = isData

	// 8 Query exact key + filter mismatch
	qs, qe := store.Query(ctx, b, &T{P: d.Key("p"), S: d.Key("f"), N: d.Eq(999)})
	check("query-exact-filter-mismatch-empty", qe == nil && len(qs) == 0, "len=%d err=%v", len(qs), qe)

	// 9 Query with limit over a large partition: how many LIST pages?
	fmt.Printf("[%s] seeding 2500 items for list-limit check...\n", *backend)
	seed(ctx, b, "big", 2500)
	c.Reset()
	q, qerr := store.Query(ctx, b, &T{P: d.Key("big"), S: d.KeyPrefix("")}, d.WithLimit(5))
	check("query-limit-stops-listing", qerr == nil && c.List.Load() == 1, "items=%d requests: %s", len(q), c)

	// 10 Query concurrent with deletes
	seed(ctx, b, "churn", 300)
	var fails, runs int
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = store.Delete(ctx, b, &T{P: d.Key("churn"), S: d.Key(fmt.Sprintf("%05d", i%300))})
			_ = store.Put(ctx, b, &T{P: d.Key("churn"), S: d.Key(fmt.Sprintf("%05d", i%300)), N: d.Set(i)})
		}
	}()
	var lastErr error
	for i := 0; i < 20; i++ {
		runs++
		if _, err := store.Query(ctx, b, &T{P: d.Key("churn"), S: d.KeyPrefix("")}); err != nil {
			fails++
			lastErr = err
		}
	}
	close(stop)
	check("query-tolerates-concurrent-delete", fails == 0, "failed %d/%d queries; last=%v", fails, runs, short(lastErr))

	// 10b Query on a partition whose items were all deleted
	_ = store.Put(ctx, b, &T{P: d.Key("emptied"), S: d.Key("1"), N: d.Set(1)})
	_ = store.Delete(ctx, b, &T{P: d.Key("emptied"), S: d.Key("1")})
	eq, eqe := store.Query(ctx, b, &T{P: d.Key("emptied"), S: d.KeyPrefix("")})
	check("query-on-emptied-partition", eqe == nil && len(eq) == 0, "len=%d err=%v", len(eq), short(eqe))
	_, ge := store.Get(ctx, b, &T{P: d.Key("emptied"), S: d.Key("")})
	check("get-empty-sort-key-not-found", errors.Is(ge, d.ErrNotFound), "Get sk=\"\" err=%v", short(ge))

	// 11 key validation
	e = store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key("a+b"), N: d.Set(1)})
	check("key-with-plus-rejected-as-error", e != nil && !errors.Is(e, store.ErrPanic), "err=%v", e)
	e = store.Create(ctx, b, &T{P: d.Key("p"), S: d.Key(strings.Repeat("x", 101)), N: d.Set(1)})
	check("key-101-chars-rejected", e != nil && !errors.Is(e, store.ErrPanic), "err=%v", e)

	// 12 context cancellation is detectable
	cctx, cc := context.WithCancel(ctx)
	cc()
	_, ce := store.Get(cctx, b, k("f"))
	check("ctx-cancel-errors-is", errors.Is(ce, context.Canceled), "errors.Is(err, context.Canceled)=%v err=%v", errors.Is(ce, context.Canceled), short(ce))

	// 13 Put on missing object with ETag
	tag := `"abc"`
	e = store.Put(ctx, b, &T{P: d.Key("p"), S: d.Key("missing"), ETag: &tag, N: d.Set(1)})
	check("put-etag-on-missing", errors.Is(e, d.ErrConcurrencyConflict), "err=%v", e)
	return nil
}

func seed(ctx context.Context, b *d.Bucket, part string, n int) {
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_ = store.Put(ctx, b, &T{P: d.Key(part), S: d.Key(fmt.Sprintf("%05d", i)), N: d.Set(i)})
		}()
	}
	wg.Wait()
}

func short(err error) string {
	if err == nil {
		return "<nil>"
	}
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func dkey(s string) d.KeyField    { return d.Key(s) }
func dprefix(s string) d.KeyField { return d.KeyPrefix(s) }
