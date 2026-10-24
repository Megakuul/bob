package orchestrator_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

func TestCheckRecord(t *testing.T) {
	be := env.Start(t)
	control, fleet := be.Open(t, "control"), be.Open(t, "fleet")
	if fleet == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	create := func(b *store.Bucket, v any) {
		t.Helper()
		var err error
		switch r := v.(type) {
		case *store.JobRecord:
			err = store.Create(ctx, b, r)
		case *store.WorkerRecord:
			err = store.Create(ctx, b, r)
		case *store.FleetConfig:
			err = store.Create(ctx, b, r)
		case *store.ControllerLease:
			err = store.Create(ctx, b, r)
		case *store.BuildRecord:
			err = store.Create(ctx, b, r)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := func(b *store.Bucket, key string, body []byte) {
		t.Helper()
		resp, err := be.FS.Do(ctx, be.FS.Root, http.MethodPut, "/"+b.Name()+"/"+key, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	create(fleet, &store.JobRecord{ID: store.ID("jok"), State: store.Set[string]("running"), Owner: store.Set[string]("w"), Epoch: store.Set[int64](1), Spec: store.Set([]byte{1})})
	create(fleet, &store.JobRecord{ID: store.ID("jnoowner"), State: store.Set[string]("claimed"), Spec: store.Set([]byte{1})})
	create(fleet, &store.JobRecord{ID: store.ID("jbad"), State: store.Set[string]("weird"), Spec: store.Set([]byte{1})})
	raw(fleet, "job/jgarbage", []byte{0xff, 0x00})
	create(fleet, &store.WorkerRecord{Wid: store.ID("w"), State: store.Set[string]("active")})
	create(fleet, &store.FleetConfig{Name: store.ID("cfg"), Members: store.Set([]store.FleetMember{{Orch: "a"}, {Orch: "a"}})})
	create(fleet, &store.ControllerLease{Name: store.ID("x"), Owner: store.Set[string]("a")})
	create(control, &store.BuildRecord{ID: store.ID("bok"), State: store.Set[string]("succeeded")})
	create(control, &store.BuildRecord{ID: store.ID("bbad"), State: store.Set[string]("x")})
	enc := func(v any) []byte {
		b, err := cbor.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	raw(control, "pidx/all/p/0000000000", enc(map[string]int{"n": 1}))
	raw(control, "pidx/all/p/0000000001", []byte{0xa1, 0x61})
	for name, c := range map[string]struct {
		b    *store.Bucket
		role string
		key  string
		want string // "" | "corrupt" | "invalid"
	}{
		"job ok":            {fleet, "fleet", "job/jok", ""},
		"job no owner":      {fleet, "fleet", "job/jnoowner", "invalid"},
		"job bad state":     {fleet, "fleet", "job/jbad", "invalid"},
		"job garbage":       {fleet, "fleet", "job/jgarbage", "corrupt"},
		"worker ok":         {fleet, "fleet", "worker/w", ""},
		"member twice":      {fleet, "fleet", "fleet/cfg", "invalid"},
		"lease epoch 0":     {fleet, "fleet", "ctl/x", "invalid"},
		"build ok":          {control, "control", "build/bok", ""},
		"build bad state":   {control, "control", "build/bbad", "invalid"},
		"other well-formed": {control, "control", "pidx/all/p/0000000000", ""},
		"other truncated":   {control, "control", "pidx/all/p/0000000001", "corrupt"},
	} {
		err := orchestrator.CheckRecord(ctx, c.b, c.role, c.key)
		got := ""
		if err != nil {
			got, _, _ = strings.Cut(err.Error(), ":")
		}
		if got != c.want {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if err := orchestrator.CheckRecord(ctx, fleet, "fleet", "job/missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a missing record = %v, want ErrNotFound", err)
	}
	problems, err := orchestrator.Fsck(ctx, fleet, "fleet", []string{"job/jok", "job/jbad", "job/missing", "_bob/probe/x"})
	if err != nil || len(problems) != 1 || problems[0].Key != "job/jbad" {
		t.Errorf("Fsck = %v, %v; want the one invalid job", problems, err)
	}
}
