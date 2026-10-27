package nixidx

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/store"
)

func TestSuggest(t *testing.T) {
	names := []string{"openssl", "openssh", "openssl_3", "zlib", "hello", "python3"}
	if got := Suggest("opensll", names); len(got) == 0 || got[0] != "openssl" {
		t.Errorf("Suggest(opensll) = %v, want openssl first", got)
	}
	if got := Suggest("pyhton3.requests", names); !slices.Equal(got, []string{"python3"}) {
		t.Errorf("Suggest(pyhton3.requests) = %v", got)
	}
	if got := Suggest("qqqqqqqq", names); len(got) != 0 {
		t.Errorf("Suggest(qqqqqqqq) = %v, want none", got)
	}
	if got := Suggest("open", names); len(got) != 3 {
		t.Errorf("Suggest(open) = %v, want 3 (prefix matches)", got)
	}
}

func TestDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"kitten", "sitting", 3}, {"a", "a", 0}, {"", "ab", 2}, {"opensll", "openssl", 1}} {
		if got := distance(c.a, c.b, 5); got != c.want {
			t.Errorf("distance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := distance("abcdef", "uvwxyz", 2); got != 3 {
		t.Errorf("distance above the limit = %d, want limit+1", got)
	}
}

func TestAllowed(t *testing.T) {
	if !allowed([]string{"CVE-1"}, []string{"CVE-1", "CVE-2"}) || allowed([]string{"CVE-1", "CVE-3"}, []string{"CVE-1"}) || allowed(nil, []string{"x"}) {
		t.Errorf("allowed is wrong")
	}
}

// TestCheck runs the pre-checks against the miniature nixpkgs of tests/nix/testdata/fakepkgs.
func TestCheck(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Skipf("no store on this host: %v", err)
	}
	src, _ := filepath.Abs(filepath.Join("..", "..", "..", "tests", "nix", "testdata", "fakepkgs"))
	sys := map[string]string{"arm64": "aarch64-linux", "amd64": "x86_64-linux"}[runtime.GOARCH]
	// At bob's surface revision a missing attribute is the plain 1001 with suggestions.
	idx, err := Open(ctx, st, Config{Source: src, Rev: lock.DefaultRev, System: sys})
	if err != nil {
		t.Fatal(err)
	}
	pos := diag.Pos{File: "libs/foo/bob.go", Line: 7}
	need := func(attr string) Need { return Need{Attr: attr, Label: "nixpkgs:" + attr, DefinedAt: pos} }
	insecureOK := need("insecure-pkg")
	insecureOK.AllowInsecure = []string{"CVE-2099-0001"}
	unfreeOK := need("unfree-pkg")
	unfreeOK.AllowUnfree = true
	_, err = idx.Check(ctx, []Need{need("hello"), need("helo"), need("broken-pkg"), need("insecure-pkg"),
		need("unfree-pkg"), need("wrong-system"), insecureOK, unfreeOK})
	got := map[string]diag.Code{}
	for _, e := range flatten(err) {
		var d *diag.Error
		if !errors.As(e, &d) {
			t.Fatalf("not a diag.Error: %v", e)
		}
		if d.DefinedAt != pos {
			t.Errorf("%s: definedAt %v", d.Code.ID(), d.DefinedAt)
		}
		got[d.Args["attr"]] = d.Code
		if d.Code == 1001 && d.Hint() != "did you mean hello?" {
			t.Errorf("1001 hint %q", d.Hint())
		}
	}
	want := map[string]diag.Code{"helo": 1001, "broken-pkg": 1002, "insecure-pkg": 1003, "unfree-pkg": 1004, "wrong-system": 1005}
	if !maps.Equal(got, want) {
		t.Errorf("findings %v, want %v (err %v)", got, want, err)
	}
	// At another pin, a missing top-level attribute is a typed handle the pin lacks (R-NIX-032).
	other, err := Open(ctx, st, Config{Source: src, Rev: "check", System: sys})
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Check(ctx, []Need{need("helo")})
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1001 || d.Args["variant"] != "pin" ||
		d.Error() != `libs/foo/bob.go:7: nixpkgs at the pinned revision check has no package "helo" (nixpkgs.Helo)` {
		t.Errorf("pin variant: %v", err)
	}
}
