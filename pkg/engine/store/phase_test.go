package store

import "testing"

func TestPhaseText(t *testing.T) {
	const h = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-"
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"nix-store", []string{"--realise", h + "go-1.26.8.drv"}, "fetching or building go-1.26.8"},
		{"nix-store", []string{"--realise", "--add-root", "/p/root", h + "a-1.drv", h + "b-2.drv", h + "c-3.drv", h + "d-4.drv"}, "fetching or building a-1, b-2 and 2 more"},
		{"nix", []string{"build", h + "gopls-0.22.0.drv^out"}, "fetching or building gopls-0.22.0"},
		{"nix-store", []string{"--import"}, "importing into bob's store"},
		{"nix", []string{"path-info", "x"}, "working in bob's store"},
	} {
		if got := phaseText(c.name, c.args); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.name, c.args, got, c.want)
		}
	}
}
