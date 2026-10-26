package eval

import "testing"

// TestEvalModuleSelf: in bob's own repository the evaluator's main module is not the
// framework module, so bob.go files compile against the running bob's framework (A154).
func TestEvalModuleSelf(t *testing.T) {
	if got := evalModule(frameworkModule); got == frameworkModule || got != selfModule {
		t.Errorf("evalModule(%q) = %q, want %q", frameworkModule, got, selfModule)
	}
	if got := evalModule("example.com/acme"); got != "example.com/acme" {
		t.Errorf("evalModule(example.com/acme) = %q", got)
	}
}

// In bob's own repository a bob.go package is imported by the framework module's path
// (cmd/bob/bob.go imports the root package); framework packages have no bob.go (A545).
func TestSelfBobImport(t *testing.T) {
	own := map[string]*bobFile{".": {}, "cmd/bob": {}}
	for _, c := range []struct {
		module, p string
		want      bool
	}{
		{frameworkModule, frameworkModule, true},
		{frameworkModule, frameworkModule + "/cmd/bob", true},
		{frameworkModule, frameworkModule + "/pkg/bob", false},
		{"example.com/acme", frameworkModule, false},
		{frameworkModule, "example.com/other", false},
	} {
		if got := selfBobImport(c.module, own, c.p); got != c.want {
			t.Errorf("selfBobImport(%q, %q) = %v, want %v", c.module, c.p, got, c.want)
		}
	}
}
