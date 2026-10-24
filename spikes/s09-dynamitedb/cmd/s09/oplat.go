package main

import (
	"context"
	"fmt"
	"time"

	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/store"
)

func oplat(ctx context.Context) error {
	c := &store.Counter{}
	cn := conn(ctx, "s09-oplat", c, 0, true)
	b := cn.Bucket
	n := 200
	seed(ctx, b, "ten", 10)
	run := func(name string, f func(i int) error) {
		var xs []time.Duration
		c.Reset()
		errs := 0
		for i := 0; i < n; i++ {
			t := time.Now()
			if err := f(i); err != nil {
				errs++
			}
			xs = append(xs, time.Since(t))
		}
		fmt.Printf("[%s] %-28s p50=%-8v p90=%-8v p99=%-8v req/op=%.1f errs=%d\n", *backend, name, pct(xs, 50), pct(xs, 90), pct(xs, 99), float64(c.Total())/float64(n), errs)
	}
	run("Create", func(i int) error { return store.Create(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i)), N: d.Set(i)}) })
	run("Get", func(i int) error { _, err := store.Get(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i))}); return err })
	run("Put", func(i int) error { return store.Put(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i)), N: d.Set(i)}) })
	run("Update(Increment)", func(i int) error { return store.Update(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i)), N: d.Increment(1)}) })
	run("Get+Put(ETag) CAS", func(i int) error {
		g, err := store.Get(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i))})
		if err != nil {
			return err
		}
		return store.Put(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i)), ETag: g.ETag, N: d.Set(g.N.Value() + 1)})
	})
	run("Query 10 items", func(i int) error { _, err := store.Query(ctx, b, &T{P: d.Key("ten"), S: d.KeyPrefix("")}); return err })
	run("ListKeys 10 items", func(i int) error { _, _, err := cn.ListKeys(ctx, "t/ten/s/", "", 0); return err })
	run("Query empty partition", func(i int) error { _, err := store.Query(ctx, b, &T{P: d.Key("none"), S: d.KeyPrefix("")}); return err })
	run("Delete", func(i int) error { return store.Delete(ctx, b, &T{P: d.Key("o"), S: d.Key(fmt.Sprint(i))}) })
	return nil
}
