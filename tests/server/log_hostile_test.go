package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// TestRLOG102HostileOutput is the black-box half of R-LOG-102: what a hostile *build* can make a worker
// put into its log stream (a 100 KB line, 6,000 lines in one burst, a flood of tens of MiB, terminal
// escapes and markup). The rejection of forged LiveChunks (cross-job chunk, stale epoch, oversize chunk
// from a hostile *worker*) needs an Attach client with a registered worker key and is not covered here
// (SPEC problem in the M1.8 report). Follower output of an unrelated build must be unchanged, and no
// follower may ever be handed a line over 4 KiB.
func TestRLOG102HostileOutput(t *testing.T) {
	parity.Covers(t, "R-LOG-102")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 2, Workers: 2, Env: fastFleet(map[string]string{"BOB_WORKER_SLOTS": "2"})})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	repo := srvtest.Repo(t, []srvtest.Pkg{
		{Name: "evil", Cmd: `head -c 100000 /dev/zero | tr '\0' 'A'; echo; seq 1 6000; ` +
			`echo '<script>alert(1)</script>'; printf '\033[2J\033]0;owned\007\n'; ` +
			`head -c 40000000 /dev/zero | tr '\0' 'x' | fold -w 100; echo evil-done`},
		{Name: "good", Cmd: stamped(20)},
	})
	good, err := e.Submit(repo, "//good:t")
	if err != nil {
		t.Fatal(err)
	}
	st := srvtest.NewStream()
	fdone := make(chan error, 1)
	go func() { fdone <- drain(ctx, e.Via(1).Follow(good, "//good:t"), st, nil) }()
	evil, err := e.Submit(repo, "//evil:t")
	if err != nil {
		t.Fatal(err)
	}
	evilStream := srvtest.NewStream()
	edone := make(chan error, 1)
	go func() { edone <- drain(ctx, e.Via(0).Follow(evil, "//evil:t"), evilStream, nil) }()

	if err := <-fdone; err != nil {
		t.Errorf("follower of the unrelated build: %v", err)
	}
	for _, s := range st.Errs {
		t.Errorf("unrelated build changed by the hostile one: %s", s)
	}
	if n := len(st.Lines["//good:t"]); n != 200*20 {
		t.Errorf("unrelated build: follower got %d lines, want %d", n, 200*20)
	}
	<-edone // the hostile stream may end with an error; what matters is what it carried
	for _, l := range evilStream.Lines["//evil:t"] {
		if len(l.Text) > 4096 {
			t.Fatalf("a follower was handed a %d byte line (seq %d), limit is 4 KiB", len(l.Text), l.Seq)
		}
	}
	if _, err := e.WaitBuild(evil, 3*time.Minute); err != nil {
		t.Errorf("hostile build never became terminal: %v", err)
	}
	// log text is data: the stored lines are the build's bytes, unmodified and never interpreted
	ls, err := e.Log(ctx, evil, "//evil:t", 0)
	if err != nil {
		t.Fatal(err)
	}
	var script bool
	for _, l := range ls {
		if len(l.Text) > 4096 {
			t.Errorf("persisted line %d has %d bytes, limit is 4 KiB", l.Seq, len(l.Text))
		}
		script = script || strings.Contains(l.Text, "<script>alert(1)</script>")
	}
	if !script {
		t.Errorf("the markup line is missing from the persisted log")
	}
	// the orchestrators survived: both still members, no panic in their logs
	if orchs, err := e.Orchestrators(ctx); err != nil || len(orchs) != 2 {
		t.Errorf("orchestrators after the hostile build: %v, %v", orchs, err)
	}
	for _, p := range e.Stack.Info.Orchestrators {
		b, _ := os.ReadFile(p.Log)
		if strings.Contains(string(b), "panic:") || strings.Contains(string(b), "goroutine 1 [") {
			t.Errorf("%s crashed or panicked:\n%.2000s", p.Name, b)
		}
	}
	e.AssertNoList()
}
