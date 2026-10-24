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
