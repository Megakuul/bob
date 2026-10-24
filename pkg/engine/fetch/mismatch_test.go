package fetch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// BOB-E-3001 names the URL, the pinned and the downloaded hash in what bob shows (message,
// label, excerpt), not only in the arguments (I-M7a NEEDS 5, R-CC-005a).
func TestMismatchShowsURLAndHashes(t *testing.T) {
	e := newEnv(t)
	good, bad := []byte("payload\n"), []byte("payloaD\n")
	u := e.serve("/f", 200, bad)
	_, err := e.f.Ensure(context.Background(), Request{URLs: []string{u}, Kind: File, Hash: sriOf(good), Name: "p", Label: "//third_party:p"})
	var de *diag.Error
	if !errors.As(err, &de) || de.Code != 3001 {
		t.Fatalf("error %v, want BOB-E-3001", err)
	}
	text := strings.Join(append([]string{de.Error(), de.Label}, de.Excerpt...), "\n")
	for _, w := range []string{"//third_party:p", u, sriOf(good), sriOf(bad)} {
		if !strings.Contains(text, w) {
			t.Errorf("BOB-E-3001 does not show %q:\n%s", w, text)
		}
	}
	want := []string{"url:        " + u, "bob.lock:   " + sriOf(good), "downloaded: " + sriOf(bad)}
	if strings.Join(de.Excerpt, "\n") != strings.Join(want, "\n") {
		t.Errorf("excerpt %q, want %q", de.Excerpt, want)
	}
}
