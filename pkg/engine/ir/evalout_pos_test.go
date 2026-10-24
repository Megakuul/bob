package ir

import "testing"

// A code-less definition error with a position reads "<file>:<line>: invalid build
// definition: <rest>" (I-M7a NEEDS 3), the position shown once.
func TestDefinitionErrorPosition(t *testing.T) {
	stderr := `bob:: {"type":"diag","severity":"error","label":"//libs/mathx:mathx","file":"libs/mathx/bob.go","line":12,"msg":"libs/mathx/bob.go:12: //libs/mathx:mathx: flag \"-I/nix/store/x\" is not allowed"}` + "\n"
	errs, other := Diagnostics([]byte(stderr), func(f string) string { return f })
	if len(errs) != 1 || len(other) != 0 {
		t.Fatalf("errors %v, other %v", errs, other)
	}
	e := errs[0]
	want := `libs/mathx/bob.go:12: invalid build definition: //libs/mathx:mathx: flag "-I/nix/store/x" is not allowed`
	if e.Error() != want {
		t.Errorf("message %q, want %q", e.Error(), want)
	}
	if e.Label != "//libs/mathx:mathx" || e.Location == nil || e.Location.File != "libs/mathx/bob.go" || e.Location.Line != 12 {
		t.Errorf("label %q, location %+v", e.Label, e.Location)
	}
}
