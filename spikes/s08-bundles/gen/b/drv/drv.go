// Package drv computes Nix derivations (ATerm, hashDerivationModulo, store paths) in Go,
// so backend B never needs the Nix language for bob's own packages.
package drv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const StoreDir = "/nix/store"

// Derivation is an input-addressed or fixed-output derivation.
type Derivation struct {
	Name      string
	System    string
	Builder   string
	Args      []string
	Env       map[string]string
	InputDrvs map[string][]string // drv path -> outputs used
	InputSrcs []string            // store paths

	// Fixed-output (single output "out") when FixedHash != "".
	FixedAlgo string // "sha256"
	FixedMode string // "flat" | "recursive"
	FixedHash []byte // raw digest

	// Computed by Finalize.
	OutPath string
	DrvPath string
	modulo  [32]byte
}

// Resolver returns the hashDerivationModulo of an input derivation (for derivations bob made)
// or ok=false for nixpkgs derivations, whose modulo hash the caller supplies via Known.
type Resolver interface {
	Modulo(drvPath string) ([32]byte, bool)
}

func (d *Derivation) IsFixed() bool { return len(d.FixedHash) > 0 }

// Finalize computes output path, env["out"], modulo hash and drv path.
func (d *Derivation) Finalize(r Resolver) error {
	if d.Env == nil {
		d.Env = map[string]string{}
	}
	if d.IsFixed() {
		d.OutPath = FixedOutputPath(d.Name, d.FixedMode, d.FixedHash)
		d.Env["out"] = d.OutPath
		algo := d.FixedAlgo
		if d.FixedMode == "recursive" {
			algo = "r:" + algo
		}
		d.modulo = sha256.Sum256([]byte("fixed:out:" + algo + ":" + hex.EncodeToString(d.FixedHash) + ":" + d.OutPath))
	} else {
		d.OutPath, d.Env["out"] = "", ""
		h, err := d.hashModulo(r)
		if err != nil {
			return err
		}
		d.OutPath = MakeStorePath("output:out", h, d.Name)
		d.Env["out"] = d.OutPath
		if d.modulo, err = d.hashModulo(r); err != nil {
			return err
		}
	}
	refs := append([]string{}, d.InputSrcs...)
	for p := range d.InputDrvs {
		refs = append(refs, p)
	}
	sort.Strings(refs)
	t := "text"
	for _, ref := range refs {
		t += ":" + ref
	}
	d.DrvPath = MakeStorePath(t, sha256.Sum256([]byte(d.ATerm(nil))), d.Name+".drv")
	return nil
}

func (d *Derivation) Modulo() [32]byte { return d.modulo }

func (d *Derivation) hashModulo(r Resolver) ([32]byte, error) {
	repl := map[string]string{}
	for p := range d.InputDrvs {
		m, ok := r.Modulo(p)
		if !ok {
			return [32]byte{}, fmt.Errorf("drv %s: no modulo hash known for input %s", d.Name, p)
		}
		repl[p] = hex.EncodeToString(m[:])
	}
	return sha256.Sum256([]byte(d.ATerm(repl))), nil
}

// ATerm serialises the derivation. repl replaces input drv paths (for hashDerivationModulo).
func (d *Derivation) ATerm(repl map[string]string) string {
	var b strings.Builder
	b.WriteString("Derive([")
	if d.IsFixed() {
		algo := d.FixedAlgo
		if d.FixedMode == "recursive" {
			algo = "r:" + algo
		}
		fmt.Fprintf(&b, "(%s,%s,%s,%s)", q("out"), q(d.OutPath), q(algo), q(hex.EncodeToString(d.FixedHash)))
	} else {
		fmt.Fprintf(&b, "(%s,%s,%s,%s)", q("out"), q(d.OutPath), q(""), q(""))
	}
	b.WriteString("],[")
	type in struct {
		k    string
		outs []string
	}
	var ins []in
	for p, outs := range d.InputDrvs {
		k := p
		if repl != nil {
			k = repl[p]
		}
		o := append([]string{}, outs...)
		sort.Strings(o)
		ins = append(ins, in{k, o})
	}
	sort.Slice(ins, func(i, j int) bool { return ins[i].k < ins[j].k })
	// With replacement, two drvs could map to the same hash; Nix merges outputs then. Ignored in spike.
	for i, x := range ins {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%s,%s)", q(x.k), list(x.outs))
	}
	srcs := append([]string{}, d.InputSrcs...)
	sort.Strings(srcs)
	fmt.Fprintf(&b, "],%s,%s,%s,%s,[", list(srcs), q(d.System), q(d.Builder), list(d.Args))
	keys := make([]string, 0, len(d.Env))
	for k := range d.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%s,%s)", q(k), q(d.Env[k]))
	}
	b.WriteString("])")
	return b.String()
}

func list(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = q(x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func q(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// MakeStorePath implements Nix's makeStorePath.
func MakeStorePath(typ string, h [32]byte, name string) string {
	s := typ + ":sha256:" + hex.EncodeToString(h[:]) + ":" + StoreDir + ":" + name
	full := sha256.Sum256([]byte(s))
	return StoreDir + "/" + base32(compress(full[:], 20)) + "-" + name
}

// FixedOutputPath implements makeFixedOutputPath for sha256.
func FixedOutputPath(name, mode string, h []byte) string {
	var hh [32]byte
	copy(hh[:], h)
	if mode == "recursive" {
		return MakeStorePath("source", hh, name)
	}
	inner := sha256.Sum256([]byte("fixed:out:sha256:" + hex.EncodeToString(h) + ":"))
	return MakeStorePath("output:out", inner, name)
}

func compress(h []byte, n int) []byte {
	out := make([]byte, n)
	for i, b := range h {
		out[i%n] ^= b
	}
	return out
}

const b32chars = "0123456789abcdfghijklmnpqrsvwxyz"

func base32(h []byte) string {
	n := (len(h)*8 + 4) / 5
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b := uint(i * 5)
		j, k := b/8, b%8
		c := uint(h[j]) >> k
		if int(j)+1 < len(h) {
			c |= uint(h[j+1]) << (8 - k)
		}
		out[n-1-i] = b32chars[c&0x1f]
	}
	return string(out)
}

func base(p string) string { return strings.TrimPrefix(p, StoreDir+"/") }

// JSON returns the `nix derivation add` (format version 4) representation.
func (d *Derivation) JSON() ([]byte, error) {
	out := map[string]any{"path": base(d.OutPath)}
	if d.IsFixed() {
		out = map[string]any{"method": map[bool]string{true: "nar", false: "flat"}[d.FixedMode == "recursive"],
			"hash": "sha256-" + b64(d.FixedHash)}
	}
	drvs := map[string]any{}
	for p, outs := range d.InputDrvs {
		drvs[base(p)] = map[string]any{"outputs": outs, "dynamicOutputs": map[string]any{}}
	}
	srcs := make([]string, 0, len(d.InputSrcs))
	for _, s := range d.InputSrcs {
		srcs = append(srcs, base(s))
	}
	args := d.Args
	if args == nil {
		args = []string{}
	}
	return json.Marshal(map[string]any{
		"name": d.Name, "version": 4, "system": d.System, "builder": d.Builder, "args": args, "env": d.Env,
		"inputs":  map[string]any{"drvs": drvs, "srcs": srcs},
		"outputs": map[string]any{"out": out},
	})
}
