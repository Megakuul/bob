package bob

import (
	"slices"
	"strings"
	"testing"
)

// restricted is a kind constructor with a Platforms option (ext.Restrict).
func restricted(p *P, name string, allowed []Platform, deps ...Package) *thing {
	t := mk(p, "test.thing", name, deps...)
	restrict(t.n, allowed)
	return t
}

func TestPlatformRestriction(t *testing.T) {
	isolate(t)
	foo := Define(func(p *P) *thing { return restricted(p, "foo", []Platform{LinuxARM64}) })
	Define(func(p *P) *thing { return mk(p, "test.thing", "app", foo(p)) })

	evalOK(t, "//:app@linux-arm64")
	_, diags := run(reg, env(), request{Targets: []string{"//:app@linux-amd64"}, Host: "linux-arm64", Root: testDir()})
	if len(diags) != 1 {
		t.Fatalf("diags = %+v, want one", diags)
	}
	d := diags[0]
	if d.code != 6 || d.msg != "//:foo does not support linux-amd64 (it allows: linux-arm64)" || d.label != "//:foo" {
		t.Errorf("diag = %+v", d)
	}
	if !slices.Equal(d.chain, []string{"//:app", "//:foo"}) {
		t.Errorf("chain = %v", d.chain)
	}
	if d.pos.File == "" || d.pos.Line == 0 {
		t.Errorf("no definedAt: %+v", d.pos)
	}
}

func TestPlatformRestrictionUnknownPlatform(t *testing.T) {
	isolate(t)
	Define(func(p *P) *thing { return restricted(p, "foo", []Platform{"linux-riscv64"}) })
	msgs := evalErr(t, "//:foo")
	if len(msgs) != 1 || !strings.Contains(msgs[0], `unknown platform "linux-riscv64" in Platforms`) {
		t.Errorf("errors = %q", msgs)
	}
}
