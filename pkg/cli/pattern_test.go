package cli

import "testing"

// TestResolvePattern (A455, SPEC R-FW-015): the relative forms resolve against the current
// directory and the short form "//dir" names "//dir:<base>". Before, they reached the
// evaluator unchanged ("invalid label \":hello\"") or matched no package (`bob run //dir`).
func TestResolvePattern(t *testing.T) {
	tests := []struct{ rel, in, want string }{
		{"cmd/hello", ":hello", "//cmd/hello:hello"},
		{"", ":hello", "//:hello"},
		{"cmd/hello", ".", "//cmd/hello:all"},
		{"", ".", "//:all"},
		{"cmd", "./...", "//cmd/..."},
		{"", "./...", "//..."},
		{"cmd", "./hello", "//cmd/hello:hello"},
		{"cmd", "./hello:x", "//cmd/hello:x"},
		{"cmd", "./hello/...", "//cmd/hello/..."},
		{"", "./hello", "//hello:hello"},
		{"x", "//cmd/hello", "//cmd/hello:hello"},
		{"x", "//cmd/hello:hello", "//cmd/hello:hello"},
		{"x", "//...", "//..."},
		{"x", "//cmd/...", "//cmd/..."},
		{"x", "//cmd:all", "//cmd:all"},
		{"x", "//api:api@linux-amd64", "//api:api@linux-amd64"},
		{"x", "nixpkgs:openssl", "nixpkgs:openssl"},
		{"x", "example.com/o//libs/bar:bar", "example.com/o//libs/bar:bar"},
	}
	for _, tt := range tests {
		if got := resolvePattern(tt.rel, tt.in); got != tt.want {
			t.Errorf("resolvePattern(%q, %q) = %q, want %q", tt.rel, tt.in, got, tt.want)
		}
	}
}
