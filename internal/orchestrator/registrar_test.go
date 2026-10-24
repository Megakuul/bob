package orchestrator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// Any worker may overwrite wreg/<wid> (S11 §3.4): a registration document that does not decode
// is a rejection of that registration, not an error that stops the registrar pass.
func TestRegistrarGarbageRegistration(t *testing.T) {
	be := env.Start(t)
	fleet := be.Open(t, "fleet")
	if fleet == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	resp, err := be.FS.Do(ctx, be.FS.Root, http.MethodPut, "/"+fleet.Name()+"/wreg/wgarbage", nil, []byte{0xff, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	now := time.Now()
	if _, err := store.RegLog(fleet).Append(ctx, &store.RegSlot{Wid: store.Set("wgarbage"), At: store.Set(now)}); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := orchestrator.NewRegistrar(orchestrator.RegistrarConfig{Fleet: fleet, Name: "o1", Key: key, Kid: "k1",
		Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	res, err := reg.Pass(ctx)
	if err != nil {
		t.Fatalf("pass: %v, want the garbage registration rejected", err)
	}
	want := []string{"worker registration rejected: registration record unreadable"}
	if res.Approved != 0 || !slices.Equal(res.Rejected, want) {
		t.Errorf("pass = %+v, want rejected %q", res, want)
	}
}
