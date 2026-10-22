package ir

import (
	"errors"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

func TestDiagnostics(t *testing.T) {
	stderr := strings.Join([]string{
		`bob:: {"type":"diag","severity":"error","code":"BOB-E-1006","file":"a/bob.go","line":3,"msg":"dependency cycle: //a:a (a/bob.go:3) → //a:a (a/bob.go:3)"}`,
		`bob:: {"type":"diag","severity":"error","label":"//b:b","file":"b/bob.go","line":7,"msg":"b/bob.go:7: label //b:b is defined twice"}`,
		`bob:: {"type":"diag","severity":"error","code":"BOB-E-0003","msg":"pattern //x:y matches no package"}`,
		`bob:: {"type":"diag","severity":"error","code":"BOB-E-7777","msg":"made up"}`,
		`hello from a bob.go`,
		`bob:: not json`,
	}, "\n")
	errs, other := Diagnostics([]byte(stderr), func(f string) string { return f })
	if len(errs) != 4 || len(other) != 2 {
		t.Fatalf("errs %v, other %q", errs, other)
	}
	if errs[0].Code != 1006 || errs[0].Args["cycle"] != "//a:a (a/bob.go:3) → //a:a (a/bob.go:3)" || errs[0].DefinedAt.File != "a/bob.go" {
		t.Errorf("cycle: %+v", errs[0])
	}
	if errs[1].Code != 1020 || errs[1].Error() != "b/bob.go:7: invalid build definition: label //b:b is defined twice" || errs[1].Label != "//b:b" {
		t.Errorf("code-less: %q %+v", errs[1].Error(), errs[1])
	}
	if errs[2].Code != 3 || errs[2].Error() != "pattern //x:y matches no package" || errs[2].Hint() == "" {
		t.Errorf("pattern: %+v", errs[2])
	}
	if errs[3].Code != 1020 { // unknown code: never trusted
		t.Errorf("unknown code: %+v", errs[3])
	}
	var d *diag.Error
	if !errors.As(error(errs), &d) || d.Code != 1006 {
		t.Errorf("errors.As through Errors: %v", d)
	}
}

func TestEvalErrorTemplates(t *testing.T) {
	if got := RuleError("R1", "dir", "lib").Error(); !strings.HasPrefix(got, `lib/bob.go: the first line must be "//go:build bob"`) {
		t.Errorf("R1: %q", got)
	}
	if got := Nondeterministic("//n:n", "nodes[0].deps").Error(); got != "evaluating bob.go is not deterministic: //n:n differs between two runs (field nodes[0].deps)" {
		t.Errorf("1018: %q", got)
	}
	e := DefinitionError("cmd/api/bob.go", 14, 13, "", "cannot use x")
	if e.Error() != "cmd/api/bob.go:14:13: invalid build definition: cannot use x" || e.Location == nil || e.Location.Col != 13 {
		t.Errorf("1020: %q %+v", e.Error(), e.Location)
	}
	if c := EvaluatorBug("x").Code.Class(); c != diag.ClassInternal {
		t.Errorf("EvaluatorBug class %v", c)
	}
	if got := FrameworkTooOld("example.com/dep", "v1.3.0", "v1.2.0").Error(); got != "example.com/dep: toolchain mismatch: needs bob v1.3.0 or newer; this is bob v1.2.0" {
		t.Errorf("1021: %q", got)
	}
}
