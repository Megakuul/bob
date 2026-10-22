package gen

import (
	"slices"
	"testing"

	"github.com/megakuul/bob/pkg/engine/lock"
)

// TestKeptPins: bob lock keeps every pin; --update moves all (no names, or "rust") or only the
// named crates.
func TestKeptPins(t *testing.T) {
	prev := &lock.RustSection{Crates: []lock.Crate{{Name: "serde", Version: "1.0.200"}, {Name: "itoa", Version: "1.0.11"}, {Name: "itoa", Version: "0.4.8"}}}
	all := []string{"serde@1.0.200", "itoa@1.0.11", "itoa@0.4.8"}
	cases := []struct {
		name   string
		update []string
		want   []string
	}{
		{"no update", nil, all},
		{"update all", []string{}, nil},
		{"update rust", []string{"rust"}, nil},
		{"update other ecosystem's name", []string{"golang.org/x/net"}, all},
		{"update one crate", []string{"itoa"}, []string{"serde@1.0.200"}},
		{"update one label", []string{"crates.io:serde"}, []string{"itoa@1.0.11", "itoa@0.4.8"}},
		{"update one version", []string{"itoa@0.4.8"}, []string{"serde@1.0.200", "itoa@1.0.11"}},
	}
	for _, c := range cases {
		if got := keptPins(prev, c.update); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if keptPins(nil, nil) != nil {
		t.Errorf("no previous section keeps pins")
	}
}
