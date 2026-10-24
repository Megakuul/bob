package bob

import (
	"testing"
)

// image is a toy multi-platform output built the way pkg/output/oci does it: the outer run asks
// for the program on every other platform with elsewhere and depends on what the nested run
// recorded.
func image(p *P, name string, prog *thing) *thing {
	key := "image:" + name
	if p.nested {
		graphRecord(p, key, prog)
		return &thing{}
	}
	deps := []Package{prog}
	for _, pl := range []Platform{LinuxAMD64, LinuxARM64} {
		if pl == p.platform {
			continue
		}
		q := elsewhere(p, pl)
		if v, ok := q.vals[key].(*thing); ok {
			deps = append(deps, v)
		}
	}
	return mk(p, "test.thing", name, deps...)
}

func graphRecord(p *P, key string, v any) {
	if p.vals == nil {
		p.vals = map[string]any{}
	}
	p.vals[key] = v
}

func TestElsewhereNested(t *testing.T) {
	isolate(t)
	calls := map[Platform]int{}
	prog := func(p *P) *thing { calls[p.platform]++; return mk(p, "test.thing", "prog") }
	Define(func(p *P) *thing { return image(p, "img", prog(p)) })
	ir := evalOK(t, "//:img")
	d := decode(t, ir)
	if len(d.Roots) != 1 {
		t.Fatalf("roots %v, want only the image", d.Roots)
	}
	var img []string
	progs := map[string]bool{}
	for _, n := range d.Nodes {
		switch n.Label {
		case "//:img":
			img = n.Deps
		case "//:prog":
			progs[n.Proj["platform"].(string)] = true
		}
	}
	if len(img) != 2 || !progs["linux-amd64"] || !progs["linux-arm64"] {
		t.Errorf("image deps %v, programs %v: want the program for both platforms", img, progs)
	}
	if calls[LinuxAMD64] != 1 || calls[LinuxARM64] != 1 {
		t.Errorf("program constructor calls %v, want one per platform", calls)
	}
	if len(d.Configs) != 1 {
		t.Errorf("configs %v: a nested configuration must not be listed", d.Configs)
	}
}

// A target of the other platform still gets the full image (the nested run has its own memo).
func TestElsewhereDoesNotShadowTargets(t *testing.T) {
	isolate(t)
	prog := func(p *P) *thing { return mk(p, "test.thing", "prog") }
	Define(func(p *P) *thing { return image(p, "img", prog(p)) })
	d := decode(t, evalOK(t, "//:img@linux-amd64", "//:img@linux-arm64"))
	if len(d.Roots) != 2 {
		t.Fatalf("roots %v, want both images", d.Roots)
	}
	for _, n := range d.Nodes {
		if n.Label == "//:img" && len(n.Deps) != 2 {
			t.Errorf("image %s has deps %v, want both programs", n.ID, n.Deps)
		}
	}
}
