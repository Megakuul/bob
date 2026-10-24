package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/store"
)

func isGET(r *http.Request) bool {
	return r.Method == http.MethodGet && !r.URL.Query().Has("list-type")
}

// panics runs each misuse/backend case in-process (wrapped) and the goroutine cases in a child process.
func panics(ctx context.Context) error {
	cn := conn(ctx, "s09-panics", nil, 0, true)
	b := cn.Bucket
	for i := 0; i < 3; i++ {
		_ = store.Put(ctx, b, &T{P: d.Key("q"), S: d.Key(fmt.Sprint(i)), N: d.Set(i)})
	}
	type tc struct {
		name string
		fn   func() error
	}
	var x int
	cases := []tc{
		{"Get with update operator in filter", func() error {
			_, err := store.Get(ctx, b, &T{P: d.Key("q"), S: d.Key("0"), N: d.Set(1)})
			return err
		}},
		{"Update with filter operator", func() error {
			return store.Update(ctx, b, &T{P: d.Key("q"), S: d.Key("0"), N: d.Eq(1)})
		}},
		{"Create with filter operator", func() error {
			return store.Create(ctx, b, &T{P: d.Key("q"), S: d.Key("new"), N: d.Eq(1)})
		}},
		{"Query exact key with update operator", func() error {
			_, err := store.Query(ctx, b, &T{P: d.Key("q"), S: d.Key("0"), N: d.Set(1)})
			return err
		}},
		{"Get on non-struct type", func() error { _, err := store.Get(ctx, b, &x); return err }},
		{"Get with nil model", func() error { _, err := store.Get[T](ctx, b, nil); return err }},
		{"Get with KeyPrefix (non-exact)", func() error {
			_, err := store.Get(ctx, b, &T{P: d.Key("q"), S: d.KeyPrefix("")})
			return err
		}},
		{"Get with missing pk", func() error { _, err := store.Get(ctx, b, &T{S: d.Key("0")}); return err }},
		{"Get, backend/proxy omits ETag header", func() error {
			cn.T.AddFault(&store.Fault{Mode: "noetag", Match: isGET, Remaining: 1})
			defer cn.T.ClearFaults()
			_, err := store.Get(ctx, b, &T{P: d.Key("q"), S: d.Key("0")})
			return err
		}},
		{"Update, backend/proxy omits ETag header", func() error {
			cn.T.AddFault(&store.Fault{Mode: "noetag", Match: isGET, Remaining: 1})
			defer cn.T.ClearFaults()
			before := cn.T.UnconditionalPuts.Load()
			err := store.Update(ctx, b, &T{P: d.Key("q"), S: d.Key("0"), N: d.Increment(1)})
			if n := cn.T.UnconditionalPuts.Load() - before; n > 0 {
				return fmt.Errorf("no panic, but Update sent %d PUT without If-Match (lost-update risk); err=%v", n, err)
			}
			return err
		}},
		{"Reading Value() of a filter operator (user code)", func() error {
			return store.Call(func() error { _ = d.Eq(1).Value(); return nil })
		}},
	}
	fmt.Printf("## in-process (store.Call wraps every dynamitedb call)\n")
	for _, c := range cases {
		err := c.fn()
		kind := "returned error"
		if errors.Is(err, store.ErrPanic) {
			kind = "PANIC recovered"
		} else if err == nil {
			kind = "no error"
		}
		fmt.Printf("[%s] %-48s %-16s %s\n", *backend, c.name, kind, firstLine(err))
	}
	fmt.Printf("## child process (panic inside dynamitedb's Query goroutines)\n")
	for _, c := range []string{"query-update-op", "query-noetag", "query-noetag-wrapped-goroutine"} {
		cmd := exec.CommandContext(ctx, os.Args[0], "-endpoint", *endpoint, "-backend", *backend, "crashchild", c)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		fmt.Printf("[%s] %-32s exit=%d  %s\n", *backend, c, code, lines[0])
	}
	return nil
}

func crashChild(ctx context.Context, c string) {
	cn := conn(ctx, "s09-panics", nil, 0, false)
	b := cn.Bucket
	var err error
	switch c {
	case "query-update-op":
		_, err = store.Query(ctx, b, &T{P: d.Key("q"), S: d.KeyPrefix(""), N: d.Set(1)})
	case "query-noetag", "query-noetag-wrapped-goroutine":
		cn.T.AddFault(&store.Fault{Mode: "noetag", Match: isGET, Remaining: -1})
		_, err = store.Query(ctx, b, &T{P: d.Key("q"), S: d.KeyPrefix("")})
	}
	fmt.Printf("survived: err=%v\n", firstLine(err))
}

func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s := strings.SplitN(err.Error(), "\n", 2)[0]
	if len(s) > 110 {
		s = s[:110] + "…"
	}
	return s
}
