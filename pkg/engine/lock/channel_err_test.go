package lock

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// TestChannelErrorIsDownloadFailure: a channel lookup that fails in the network (here: a name
// that cannot resolve, like every name inside a namespace whose /etc/resolv.conf dangles) is
// BOB-E-3002 with the cause in plain words, not an error that the CLI reports as BOB-E-9005
// "Nix stopped unexpectedly" (Q4, A471).
func TestChannelErrorIsDownloadFailure(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://bob-q4-test.invalid/nixos-26.05/git-revision", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, herr := http.DefaultClient.Do(req)
	if herr == nil {
		t.Skip("the .invalid name resolved here")
	}
	var d *diag.Error
	if !errors.As(channelError("nixos-26.05", herr), &d) {
		t.Fatalf("not a diagnostic: %v", herr)
	}
	if d.Code != 3002 || !strings.Contains(d.Error(), "cannot be resolved") {
		t.Errorf("got %d %q, want BOB-E-3002 naming the resolver", d.Code, d.Error())
	}
	if len(d.Raw) == 0 {
		t.Error("no raw text for -v")
	}
}
