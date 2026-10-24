package ir

import "testing"

// BOB-E-0006 from the evaluator keeps its dependency chain as the excerpt (R-PLAT-006).
func TestPlatformErrorChain(t *testing.T) {
	stderr := `bob:: {"type":"diag","severity":"error","code":"BOB-E-0006","label":"//libs/restricted:foo","file":"libs/restricted/bob.go","line":4,"msg":"//libs/restricted:foo does not support linux-amd64 (it allows: linux-arm64)","chain":["//apps/app:app","//libs/restricted:foo"]}` + "\n"
	errs, _ := Diagnostics([]byte(stderr), func(f string) string { return f })
	if len(errs) != 1 {
		t.Fatalf("errors %v", errs)
	}
	e := errs[0]
	if e.Code != 6 || e.Error() != "//libs/restricted:foo does not support linux-amd64 (it allows: linux-arm64)" {
		t.Errorf("error %v: %q", e.Code, e.Error())
	}
	if len(e.Excerpt) != 1 || e.Excerpt[0] != "//apps/app:app → //libs/restricted:foo" {
		t.Errorf("excerpt %q", e.Excerpt)
	}
	if e.DefinedAt.File != "libs/restricted/bob.go" || e.DefinedAt.Line != 4 {
		t.Errorf("definedAt %+v", e.DefinedAt)
	}
}
