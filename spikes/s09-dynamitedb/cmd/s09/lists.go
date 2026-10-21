package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/model"
	"bobspike/s09/internal/store"
)

// nextBuildNumber allocates a per-project build number: Get + Put(If-Match), retry on conflict.
func nextBuildNumber(ctx context.Context, b *d.Bucket, project string) (int, int, error) {
	retries := 0
	for {
		p, err := store.Get(ctx, b, &model.Project{ID: d.Key(project)})
		if err != nil {
			return 0, retries, err
		}
		n := p.NextBuild.Value() + 1
		err = store.Put(ctx, b, &model.Project{ID: d.Key(project), ETag: p.ETag, Name: d.Set(p.Name.Value()), Repo: d.Set(p.Repo.Value()), NextBuild: d.Set(n)})
		if err == nil {
			return n, retries, nil
		}
		if !errors.Is(err, d.ErrConcurrencyConflict) {
			return 0, retries, err
		}
		retries++
	}
}

func lists(ctx context.Context) error {
	c := &store.Counter{}
	cn := conn(ctx, "s09-lists-v2", c, 0, true)
	b := cn.Bucket
	now := time.Now()
	par := func(n int, f func(i int)) {
		sem := make(chan struct{}, 32)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func() { defer wg.Done(); defer func() { <-sem }(); f(i) }()
		}
		wg.Wait()
	}
	// build-number allocation under contention
	_ = store.Create(ctx, b, &model.Project{ID: d.Key("p01"), Name: d.Set("demo"), Repo: d.Set("forgejo/demo")})
	var mu sync.Mutex
	nums := map[int]int{}
	totalRetries := 0
	t0 := time.Now()
	par(20, func(i int) {
		for k := 0; k < 5; k++ {
			n, r, err := nextBuildNumber(ctx, b, "p01")
			if err != nil {
				fmt.Println("alloc:", err)
				return
			}
			mu.Lock()
			nums[n]++
			totalRetries += r
			mu.Unlock()
		}
	})
	dup := 0
	for _, v := range nums {
		if v > 1 {
			dup++
		}
	}
	fmt.Printf("[%s] build-number CAS counter: 20 allocators x 5 = %d numbers, duplicates=%d, conflict retries=%d, %v\n",
		*backend, len(nums), dup, totalRetries, time.Since(t0).Round(time.Millisecond))

	// seed
	par(20, func(i int) { _ = store.Put(ctx, b, &model.Project{ID: d.Key(fmt.Sprintf("p%02d", i+2)), Name: d.Set("x")}) })
	par(300, func(i int) {
		_ = store.Put(ctx, b, &model.Build{Project: d.Key("p01"), Num: d.Key(model.RevNum(i + 1)), Number: d.Set(i + 1),
			State: d.Set("succeeded"), Branch: d.Set("main"), Created: d.Set(now)})
	})
	par(10, func(i int) {
		_ = store.Put(ctx, b, &model.BuildRef{Kind: d.Key("running"), Ref: d.Key(model.NewRevID(now)), Project: d.Set("p01"), Num: d.Set(i)})
	})
	par(30, func(i int) {
		_ = store.Put(ctx, b, &model.BuildRef{Kind: d.Key("failed"), Ref: d.Key(model.NewRevID(now)), Project: d.Set("p01"), Num: d.Set(i)})
	})
	par(40, func(i int) {
		_ = store.Put(ctx, b, &model.Release{Project: d.Key("p01"), Version: d.Key(fmt.Sprintf("v%04d.%04d.%04d", 1, i, 0)), Public: d.Set(true)})
	})
	par(200, func(i int) { _ = store.Put(ctx, b, &model.User{ID: d.Key(fmt.Sprintf("u%04d", i)), Name: d.Set("n"), Kind: d.Set("local")}) })
	par(10, func(i int) { _ = store.Put(ctx, b, &model.Role{ID: d.Key(fmt.Sprintf("r%02d", i)), Perms: d.Set([]string{"GetBuild@*"})}) })
	par(30, func(i int) {
		_ = store.Put(ctx, b, &model.UserRole{User: d.Key("u0001"), Role: d.Key(fmt.Sprintf("r%02d", i%10)), By: d.Set("admin")})
		_ = store.Put(ctx, b, &model.RoleUser{Role: d.Key("r01"), User: d.Key(fmt.Sprintf("u%04d", i)), By: d.Set("admin")})
	})
	par(100, func(i int) {
		_ = store.Put(ctx, b, &model.Worker{ID: d.Key(fmt.Sprintf("w%03d", i)), Platform: d.Set("aarch64-linux"), State: d.Set("active"), Heartbeat: d.Set(now)})
	})
	par(40, func(i int) {
		_ = store.Put(ctx, b, &model.JobRecord{Build: d.Key("B1"), ID: d.Key(model.NewID(now)), Result: d.Set("ok")})
	})
	par(60, func(i int) {
		_ = store.Put(ctx, b, &model.LiveJob{Platform: d.Key("aarch64-linux"), ID: d.Key(model.NewID(now)), Lease: d.Set(model.Lease{State: model.JobRunning})})
		_ = store.Put(ctx, b, &model.QueueEntry{Platform: d.Key("aarch64-linux"), Job: d.Key(model.NewID(now))})
	})

	type L struct {
		name, prefix string
		q            func(after string) (int, string, error)
	}
	pg := 50
	mk := func(prefix string, f func(k string) error) func(after string) (int, string, error) {
		return func(after string) (int, string, error) {
			keys, next, err := cn.ListKeys(ctx, prefix, after, pg)
			if err != nil {
				return 0, "", err
			}
			for _, k := range keys {
				if err := f(k); err != nil && !errors.Is(err, d.ErrNotFound) {
					return 0, "", err
				}
			}
			return len(keys), next, nil
		}
	}
	sk := store.SortKeyOf
	get := func(m any) error { return nil }
	_ = get
	cases := []L{
		{"projects", "project/", nil},
		{"builds of project (newest first)", "build/p01/n/", nil},
		{"running builds (overview)", "buildidx/running/b/", nil},
		{"recent failures (overview)", "buildidx/failed/b/", nil},
		{"releases of project", "release/p01/v/", nil},
		{"jobs of build (finished)", "jobs/B1/j/", nil},
		{"live jobs per platform (queued+running)", "live/aarch64-linux/j/", nil},
		{"queue depth per platform (keys only)", "jobq/aarch64-linux/j/", nil},
		{"workers", "worker/", nil},
		{"users", "user/", nil},
		{"roles", "role/", nil},
		{"roles of user", "userrole/u0001/r/", nil},
		{"users of role", "roleuser/r01/u/", nil},
	}
	getters := map[string]func(k string) error{
		"project/":              func(k string) error { _, e := store.Get(ctx, b, &model.Project{ID: d.Key(sk(k))}); return e },
		"build/p01/n/":    func(k string) error { _, e := store.Get(ctx, b, &model.Build{Project: d.Key("p01"), Num: d.Key(sk(k))}); return e },
		"buildidx/running/b/":   func(k string) error { _, e := store.Get(ctx, b, &model.BuildRef{Kind: d.Key("running"), Ref: d.Key(sk(k))}); return e },
		"buildidx/failed/b/":    func(k string) error { _, e := store.Get(ctx, b, &model.BuildRef{Kind: d.Key("failed"), Ref: d.Key(sk(k))}); return e },
		"release/p01/v/":  func(k string) error { _, e := store.Get(ctx, b, &model.Release{Project: d.Key("p01"), Version: d.Key(sk(k))}); return e },
		"jobs/B1/j/":            func(k string) error { _, e := store.Get(ctx, b, &model.JobRecord{Build: d.Key("B1"), ID: d.Key(sk(k))}); return e },
		"live/aarch64-linux/j/": func(k string) error { _, e := store.Get(ctx, b, &model.LiveJob{Platform: d.Key("aarch64-linux"), ID: d.Key(sk(k))}); return e },
		"jobq/aarch64-linux/j/": nil,
		"worker/":               func(k string) error { _, e := store.Get(ctx, b, &model.Worker{ID: d.Key(sk(k))}); return e },
		"user/":                 func(k string) error { _, e := store.Get(ctx, b, &model.User{ID: d.Key(sk(k))}); return e },
		"role/":                 func(k string) error { _, e := store.Get(ctx, b, &model.Role{ID: d.Key(sk(k))}); return e },
		"userrole/u0001/r/":      func(k string) error { _, e := store.Get(ctx, b, &model.UserRole{User: d.Key("u0001"), Role: d.Key(sk(k))}); return e },
		"roleuser/r01/u/":        func(k string) error { _, e := store.Get(ctx, b, &model.RoleUser{Role: d.Key("r01"), User: d.Key(sk(k))}); return e },
	}
	fmt.Printf("[%s] %-40s %-24s %-6s %-22s %s\n", *backend, "UI list", "key prefix", "page1", "requests page1", "page2 (StartAfter)")
	for _, l := range cases {
		g := getters[l.prefix]
		if g == nil {
			g = func(string) error { return nil }
		}
		f := mk(l.prefix, g)
		c.Reset()
		n, next, err := f("")
		r1 := fmt.Sprintf("LIST=%d GET=%d", c.List.Load(), c.Get.Load())
		p2 := "-"
		if next != "" {
			c.Reset()
			n2, _, err2 := f(next)
			p2 = fmt.Sprintf("%d items, LIST=%d GET=%d err=%v", n2, c.List.Load(), c.Get.Load(), err2)
		}
		fmt.Printf("[%s] %-40s %-24s %-6d %-22s %s %v\n", *backend, l.name, l.prefix, n, r1, p2, errOr(err))
	}
	// same partition through dynamitedb.Query for comparison
	c.Reset()
	bs, err := store.Query(ctx, b, &model.Build{Project: d.Key("p01"), Num: d.KeyPrefix("")}, d.WithLimit(50))
	fmt.Printf("[%s] dynamitedb.Query builds of project, WithLimit(50): items=%d first=#%d %s err=%v\n", *backend, len(bs), bs[0].Number.Value(), c.String(), err)
	c.Reset()
	us, err := store.Query(ctx, b, &model.User{ID: d.KeyPrefix("")}, d.WithLimit(50))
	fmt.Printf("[%s] dynamitedb.Query users (pk-only type) WithLimit(50): items=%d %s err=%v \n", *backend, len(us), c.String(), err)
	c.Reset()
	fl, err := store.Query(ctx, b, &model.Build{Project: d.Key("p01"), Num: d.KeyPrefix(""), State: d.Eq("failed")}, d.WithLimit(10))
	fmt.Printf("[%s] dynamitedb.Query builds filtered State=failed (none match) WithLimit(10): items=%d %s err=%v (filter is client-side: scans the partition)\n", *backend, len(fl), c.String(), err)
	return nil
}

func errOr(err error) string {
	if err != nil {
		return "err=" + err.Error()
	}
	return ""
}
