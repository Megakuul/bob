package gen

import (
	"encoding/json"
	"fmt"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// variant is the Go projection of a build variant (SPEC R-FW-038): the profile (-N -l), the
// race detector, Go's address sanitiser (-asan, every package and std) and the C sanitisers
// (cgo packages and the link only).
type variant struct {
	debug bool
	race  bool
	asan  bool     // Go -asan
	csan  []string // -fsanitize names of the C code: address, undefined
}

// variantOf reads the projection of a binary or test node.
func variantOf(n *ir.Node) (variant, error) {
	var v variant
	if raw, ok := n.Proj["profile"]; ok {
		var p string
		if err := json.Unmarshal(raw, &p); err != nil {
			return v, fmt.Errorf("profile: %w", err)
		}
		v.debug = p == string(bob.Debug)
	}
	if raw, ok := n.Proj["race"]; ok {
		if err := json.Unmarshal(raw, &v.race); err != nil {
			return v, fmt.Errorf("race: %w", err)
		}
	}
	if raw, ok := n.Proj["sanitizers"]; ok {
		var ss []string
		if err := json.Unmarshal(raw, &ss); err != nil {
			return v, fmt.Errorf("sanitizers: %w", err)
		}
		for _, s := range ss {
			switch bob.Sanitizer(s) {
			case bob.ASan:
				v.asan = true
				v.csan = append(v.csan, "address")
			case bob.UBSan:
				v.csan = append(v.csan, "undefined")
			}
		}
	}
	return v, nil
}

// mode is cmd/go's instrumentation mode: "race", "asan" or "".
func (v variant) mode() string {
	switch {
	case v.race:
		return "race"
	case v.asan:
		return "asan"
	}
	return ""
}

// gcflags are the compile flags of every Go package (and of std).
func (v variant) gcflags() []string {
	var f []string
	if v.debug {
		f = append(f, "-N", "-l")
	}
	if m := v.mode(); m != "" {
		f = append(f, "-"+m)
	}
	return f
}

// cflags are the flags of cgo's C compiles and of an external link.
func (v variant) cflags() []string {
	var f []string
	if v.debug {
		f = append(f, "-O0")
	}
	if len(v.csan) > 0 {
		s := "-fsanitize=" + v.csan[0]
		for _, x := range v.csan[1:] {
			s += "," + x
		}
		f = append(f, s, "-fno-omit-frame-pointer")
	}
	return f
}

// tag is the projection tag of a derivation name: dbg, then address (Go -asan, or with c also
// undefined), then race.
func (v variant) tag(c bool) []string {
	var t []string
	if v.debug {
		t = append(t, "dbg")
	}
	if c {
		t = append(t, v.csan...)
	} else if v.asan {
		t = append(t, "address")
	}
	if v.race {
		t = append(t, "race")
	}
	return t
}

// nameParts are the parts a derivation name gets after its base (R-FW-038): the platform when
// cross, then the projection tag.
func nameParts(t engine.GoTarget, tag []string) []string {
	var out []string
	if t.Cross {
		out = append(out, string(t.Platform))
	}
	return append(out, tag...)
}
