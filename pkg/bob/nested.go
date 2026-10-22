package bob

import (
	"fmt"
	"slices"

	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

// Nested configurations let an output constructor (an OCI image, a release) use its programs
// for several platforms although it receives them as values built for its own configuration:
// ext.Elsewhere runs the constructor that is being evaluated once more for another platform,
// in a configuration of its own. The nested run creates the same nodes the normal configuration
// of that platform would (identical nodes are shared), but its configuration is never a
// target: its own result is not a root and appears in the graph only through the outer node
// that depends on what the nested run recorded (ext.Record).
func init() {
	graph.Elsewhere = func(p any, platform string) any {
		pp, _ := p.(*P)
		return elsewhere(pp, Platform(platform))
	}
	graph.Nested = func(p any) bool {
		pp, _ := p.(*P)
		return pp != nil && pp.nested
	}
	graph.Record = func(p any, key string, v any) {
		if pp, _ := p.(*P); pp != nil {
			if pp.vals == nil {
				pp.vals = map[string]any{}
			}
			pp.vals[key] = v
		}
	}
	graph.Recorded = func(p any, key string) (any, bool) {
		pp, _ := p.(*P)
		if pp == nil {
			return nil, false
		}
		v, ok := pp.vals[key]
		return v, ok
	}
}

// elsewhere evaluates the running definition for plat in a nested configuration and returns
// that configuration; for p's own platform it returns p.
func elsewhere(p *P, plat Platform) *P {
	ev := active
	if ev == nil || p == nil || p.ev != ev {
		msg := "ext.Elsewhere was called outside evaluation; call constructors only inside a bob.Define function"
		if ev != nil {
			ev.misuse(msg)
		} else {
			reg.early = append(reg.early, diagnostic{msg: msg})
		}
		return nil
	}
	if !slices.Contains(platforms, plat) {
		ev.misuse(fmt.Sprintf("%s: unknown platform %q in Platforms (known: linux-amd64, linux-arm64)", bobSite().pos, plat))
		return nil
	}
	if plat == p.platform {
		return p
	}
	top := ev.top()
	if top == nil {
		ev.misuse("ext.Elsewhere must be called inside a bob.Define function")
		return nil
	}
	q := ev.nestedConfig(plat, p.variant)
	top.def.get(q)
	return q
}

// nestedConfig returns the one nested configuration of (plat, v).
func (ev *evaluation) nestedConfig(plat Platform, v Variant) *P {
	v = v.normal()
	id := configString(plat, v)
	if p, ok := ev.nestedPs[id]; ok {
		return p
	}
	if ev.nestedPs == nil {
		ev.nestedPs = map[string]*P{}
	}
	p := &P{ev: ev, platform: plat, variant: v, id: id, nested: true}
	ev.nestedPs[id] = p
	return p
}
