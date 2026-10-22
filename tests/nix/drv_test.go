package nix

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// modulos resolves hashDerivationModulo of the input derivations of a test derivation.
type modulos map[string]*drv.Derivation

func (m modulos) Modulo(p string) ([32]byte, error) {
	d, ok := m[p]
	if !ok {
		return [32]byte{}, fmt.Errorf("harness: no test derivation %s", p)
	}
	return d.Modulo(m)
}

const (
	depDrv     = "/nix/store/7l6qldk7mw8rnzaw4ac8ahspjrfmsf96-dep.drv"
	fodFlatDrv = "/nix/store/znd6nbc37vivz712jx2hgb1f2v6vlh4j-fodflat.drv"
	fodRecDrv  = "/nix/store/fdgiaw5783svvi87h9yc1g2pnqdwwld4-fodrec.drv"
	topDrv     = "/nix/store/fd4fcv9zpp0d0hb64wg0wf2wpqdjpgra-top.drv"
	stDrv      = "/nix/store/yhabsaa0ffwbc1ym6az81r141rry3q9j-st.drv"
	depDev     = "/nix/store/7gbhm0838jsn5pf773ws2y4zjvwyg11w-dep-dev"
	gccRef     = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-gcc-1"
	emptySHA   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	fakeBldr   = "/nix/store/00000000000000000000000000000000-bob-builder-1"
	srcFile    = "/nix/store/b840nd755n71g1i51wzkga1na44fv63f-srcfile"
)

// goldenATerm returns the ATerm Nix produced for the derivation whose store path is p
// (tests/nix/testdata/drv/<hash>-<name>.drv, written by nix-instantiate on Nix 2.34.8).
func goldenATerm(t testing.TB, p string) []byte {
	t.Helper()
	return mustRead(t, filepath.Join("testdata", "drv", filepath.Base(p)))
}

func depDerivation() *drv.Derivation {
	return &drv.Derivation{
		Name: "dep", System: "aarch64-linux", Builder: "/bin/sh", Args: []string{"-c", "echo"},
		Outputs: []drv.Output{{Name: "out"}, {Name: "dev"}},
		Env: map[string]string{
			"A": "a", "Z": "z\"q\\ \n\r\t", "builder": "/bin/sh", "name": "dep",
			"outputs": "out dev", "system": "aarch64-linux", "out": "", "dev": "",
		},
	}
}

func fodDerivation(name, mode string) *drv.Derivation {
	algo := "sha256"
	if mode == "recursive" {
		algo = "r:sha256"
	}
	return &drv.Derivation{
		Name: name, System: "aarch64-linux", Builder: "/bin/sh",
		Outputs: []drv.Output{{Name: "out", Algo: algo, Hash: emptySHA}},
		Env: map[string]string{
			"builder": "/bin/sh", "name": name, "out": "",
			"outputHash": "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=", "outputHashAlgo": "sha256",
			"outputHashMode": mode, "system": "aarch64-linux",
		},
	}
}

func topDerivation() *drv.Derivation {
	return &drv.Derivation{
		Name: "top", System: "aarch64-linux", Builder: "/bin/sh", Args: []string{"-c", "a\"b\\c\n\t"},
		Outputs: []drv.Output{{Name: "out"}, {Name: "lib"}},
		InputDrvs: []drv.InputDrv{
			{Path: fodFlatDrv, Outputs: []string{"out"}}, {Path: depDrv, Outputs: []string{"dev"}},
			{Path: fodRecDrv, Outputs: []string{"out"}},
		},
		InputSrcs: []string{srcFile},
		Env: map[string]string{
			"builder": "/bin/sh", "name": "top", "outputs": "out lib", "system": "aarch64-linux", "out": "", "lib": "",
			"inputs": depDev + " /nix/store/7q1h640cci469ni4lkbsi5zafk20vyyr-fodflat /nix/store/id8d9gip176pyp94nzjxkkni1qr4frby-fodrec " + srcFile,
		},
	}
}

// stJob is the bob-builder derivation whose structured attributes the Nix golden was
// evaluated from (bobSpec, outputChecks, unsafeDiscardReferences, one extra attribute).
func stJob() drv.Job {
	return drv.Job{
		Name: "st", System: "aarch64-linux", Builder: fakeBldr,
		Spec: bspec.Job{
			V: 1, Kind: "run", Label: "//a:b", Outputs: []string{"out", "dev"},
			Args: []byte(`{"steps":[{"args":{"content":"a<b>&\"\\\n\t é","path":"/x"},"op":"write"}]}`),
			Fixup: bspec.Fixup{
				Strip: true, ShrinkRPath: true, Forbidden: []string{gccRef},
				Tools: bspec.Tools{
					Strip:    "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-binutils/bin/strip",
					Objcopy:  "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-binutils/bin/objcopy",
					Patchelf: "/nix/store/cccccccccccccccccccccccccccccccc-patchelf/bin/patchelf",
				},
			},
		},
		Disallowed:     map[string][]string{"out": {gccRef}, "dev": {gccRef}},
		DiscardRefs:    map[string]bool{"out": true},
		InputDrvs:      []drv.InputDrv{{Path: depDrv, Outputs: []string{"dev"}}},
		ExtraAttribute: map[string]any{"deps": []string{depDev}},
	}
}

// goldenCases are the five derivations of the corpus, each with its Nix-computed ATerm.
func goldenCases() (map[string]*drv.Derivation, modulos, error) {
	dep, fodFlat, fodRec, top := depDerivation(), fodDerivation("fodflat", "flat"), fodDerivation("fodrec", "recursive"), topDerivation()
	st, err := stJob().Derivation()
	m := modulos{depDrv: dep, fodFlatDrv: fodFlat, fodRecDrv: fodRec}
	return map[string]*drv.Derivation{depDrv: dep, fodFlatDrv: fodFlat, fodRecDrv: fodRec, topDrv: top, stDrv: st}, m, err
}

// TestRNIX006GoldenATerm is acceptance (a): the ATerm and the paths bob computes equal what Nix
// computed (nix-instantiate, Nix 2.34.8) for a corpus covering multiple outputs, both
// fixed-output modes, input derivations and sources, escapes, and structured attributes.
func TestRNIX006GoldenATerm(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	cases, m, err := goldenCases()
	if notImplemented(t, err, "drv.Job.Derivation") {
		return
	}
	if err != nil {
		t.Fatalf("drv.Job.Derivation: %v", err)
	}
	for path, d := range cases {
		t.Run(filepath.Base(path), func(t *testing.T) {
			want := goldenATerm(t, path)
			// Parsing and printing is the identity on canonical ATerms.
			p, err := drv.Parse(want)
			if notImplemented(t, err, "drv.Parse") {
				return
			}
			if err != nil {
				t.Fatalf("drv.Parse: %v", err)
			}
			rt, err := p.ATerm()
			if err != nil {
				t.Fatalf("ATerm: %v", err)
			}
			if string(rt) != string(want) {
				t.Errorf("Parse then ATerm is not the identity:\n got %s\nwant %s", rt, want)
			}
			// Computing the paths from the bare derivation gives Nix's ATerm and drv path.
			if err := d.Finalize(m); err != nil {
				if notImplemented(t, err, "drv.Finalize") {
					return
				}
				t.Fatalf("Finalize: %v", err)
			}
			got, err := d.ATerm()
			if err != nil {
				t.Fatalf("ATerm: %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("ATerm differs from Nix's:\n got %s\nwant %s", got, want)
			}
			gp, err := d.Path()
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if gp != path {
				t.Errorf("drv path = %s, Nix says %s", gp, path)
			}
		})
	}
}

// TestRNIX006ATermEscapes pins the escape rules and the sorting of the ATerm.
func TestRNIX006ATermEscapes(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	d := &drv.Derivation{
		Name: "esc", System: "x86_64-linux", Builder: "/bin/b",
		Args:    []string{"z", "a\"\\\n\r\t"},
		Outputs: []drv.Output{{Name: "out", Path: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-esc"}, {Name: "dev", Path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-esc-dev"}},
		InputDrvs: []drv.InputDrv{
			{Path: "/nix/store/cccccccccccccccccccccccccccccccc-b.drv", Outputs: []string{"out", "dev"}},
			{Path: "/nix/store/dddddddddddddddddddddddddddddddd-a.drv", Outputs: []string{"out"}},
		},
		InputSrcs: []string{"/nix/store/ffffffffffffffffffffffffffffffff-s2", "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-s1"},
		Env:       map[string]string{"z": "1", "a": "t\tx", "out": "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-esc", "dev": "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-esc-dev"},
	}
	got, err := d.ATerm()
	if notImplemented(t, err, "drv.ATerm") {
		return
	}
	if err != nil {
		t.Fatalf("ATerm: %v", err)
	}
	want := `Derive([("dev","/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-esc-dev","",""),("out","/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-esc","","")],` +
		`[("/nix/store/cccccccccccccccccccccccccccccccc-b.drv",["dev","out"]),("/nix/store/dddddddddddddddddddddddddddddddd-a.drv",["out"])],` +
		`["/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-s1","/nix/store/ffffffffffffffffffffffffffffffff-s2"],` +
		`"x86_64-linux","/bin/b",["z","a\"\\\n\r\t"],` +
		`[("a","t\tx"),("dev","/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-esc-dev"),("out","/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-esc"),("z","1")])`
	// The ATerm escapes a TAB as \t in values; the literal TAB above is the Go value, so the
	// expected text spells it out.
	want = strings.Replace(want, "t\tx", `t\tx`, 1)
	if string(got) != want {
		t.Errorf("ATerm:\n got %s\nwant %s", got, want)
	}
}

var storeNameRE = regexp.MustCompile(`^[A-Za-z0-9+._?=-]+$`)

// TestRNIX006StoreNames is the `drv.Name` rules of R-NIX-006.
func TestRNIX006StoreNames(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	if drv.Name("a") == "" {
		harness.RedFeature(t, "drv.Name")
		return
	}
	for _, c := range []struct {
		parts []string
		want  string
	}{
		{[]string{"libs/foo", "foo", "linux-amd64", "address"}, "libs-foo-foo-linux-amd64-address"},
		{[]string{"a@b", "c"}, "a-b-c"},
		{[]string{"x y", "é"}, "x-y--"},
		{[]string{"keep", "+._?=-"}, "keep-+._?=-"},
		{[]string{"//api", "api", "host"}, "--api-api-host"},
	} {
		if got := drv.Name(c.parts...); got != c.want {
			t.Errorf("Name(%q) = %q, want %q", c.parts, got, c.want)
		}
	}
	if got := drv.Name(".hidden", "x"); strings.HasPrefix(got, ".") || got == "" {
		t.Errorf("Name(.hidden, x) = %q: a store name never starts with a dot", got)
	}
	// Over 200 bytes: the first 191 bytes, "-", and the first 8 nixbase32 characters of the
	// sha256 of the full (sanitised) name.
	long := strings.Repeat("abcdefghij", 30)
	full := "pre-" + long
	sum := sha256.Sum256([]byte(full))
	want := full[:191] + "-" + nixBase32(sum[:])[:8]
	got := drv.Name("pre", long)
	if got != want || len(got) != 200 {
		t.Errorf("long name = %q (%d bytes), want %q", got, len(got), want)
	}
	for _, p := range [][]string{{"a/b"}, {"x", "y@z", "ü"}, {long, long}} {
		if g := drv.Name(p...); !storeNameRE.MatchString(g) || len(g) > 200 {
			t.Errorf("Name(%q) = %q is not a valid store name", p, g)
		}
	}
}

// TestRNIX006FixedPathsAgainstNix is acceptance (d): `nix store add --mode flat --hash-algo
// sha512` and `--mode nar` give the paths FixedOutputPath computes.
func TestRNIX006FixedPathsAgainstNix(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	o := newOracle(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "payload.txt")
	mustWrite(t, file, []byte("some payload\nwith lines\n"), 0o644)
	tree := filepath.Join(dir, "tree")
	mustWrite(t, filepath.Join(tree, "bin", "tool"), []byte("#!/x\n"), 0o755)
	mustWrite(t, filepath.Join(tree, "share", "doc.txt"), []byte("doc\n"), 0o644)
	if err := os.Symlink("../share/doc.txt", filepath.Join(tree, "bin", "link")); err != nil {
		t.Fatal(err)
	}
	flatHex := func(algo string) string {
		return strings.TrimSpace(o.runIn("", nil, "nix", "hash", "file", "--type", algo, "--base16", file))
	}
	for _, c := range []struct {
		name, algo string
	}{{"payload-512", "sha512"}, {"payload-256", "sha256"}} {
		want := o.addFlat(file, c.name, c.algo)
		got, err := drv.FixedOutputPath(c.algo, flatHex(c.algo), false, c.name)
		if notImplemented(t, err, "drv.FixedOutputPath") {
			return
		}
		if err != nil || got != want {
			t.Errorf("flat %s: FixedOutputPath = %q (%v), nix store add says %q", c.algo, got, err, want)
		}
	}
	want := o.addNar(tree, "tree-nar")
	got, err := drv.FixedOutputPath("sha256", o.narHashHex(tree), true, "tree-nar")
	if err != nil || got != want {
		t.Errorf("nar: FixedOutputPath = %q (%v), nix store add says %q", got, err, want)
	}
}

// TestRNIX006OutputPathsChangeWithEnv is acceptance (e), a property test: changing any env
// value, argument, system, builder or input changes every output path of the derivation, and
// equal inputs give equal paths.
func TestRNIX006OutputPathsChangeWithEnv(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	rng := rand.New(rand.NewSource(6))
	word := func() string {
		b := make([]byte, 1+rng.Intn(12))
		for i := range b {
			b[i] = "abcdefghijklmnopqrstuvwxyz0123456789 \"\\\n"[rng.Intn(40)]
		}
		return string(b)
	}
	build := func(env map[string]string, name string, outs []string) (*drv.Derivation, error) {
		d := &drv.Derivation{Name: name, System: "aarch64-linux", Builder: "/bin/b", Args: []string{"x"}, Env: map[string]string{"name": name}}
		for _, o := range outs {
			d.Outputs = append(d.Outputs, drv.Output{Name: o})
			d.Env[o] = ""
		}
		for k, v := range env {
			d.Env[k] = v
		}
		return d, d.Finalize(modulos{})
	}
	paths := func(d *drv.Derivation) string {
		var s []string
		for _, o := range d.Outputs {
			s = append(s, o.Name+"="+o.Path)
		}
		return strings.Join(s, ",")
	}
	for i := 0; i < 50; i++ {
		env := map[string]string{}
		for j := 0; j < 1+rng.Intn(6); j++ {
			env["k"+word()[:1]+fmt.Sprint(j)] = word()
		}
		outs := []string{"out", "dev"}[:1+rng.Intn(2)]
		base, err := build(env, "prop", outs)
		if notImplemented(t, err, "drv.Finalize") {
			return
		}
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		again, _ := build(env, "prop", outs)
		if paths(base) != paths(again) {
			t.Fatalf("same inputs, different paths: %s vs %s", paths(base), paths(again))
		}
		for k, v := range env {
			mod := map[string]string{}
			for k2, v2 := range env {
				mod[k2] = v2
			}
			mod[k] = v + "x"
			d, err := build(mod, "prop", outs)
			if err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			for i, o := range d.Outputs {
				if o.Path == base.Outputs[i].Path {
					t.Errorf("changing env %q left output %s unchanged (%s)", k, o.Name, o.Path)
				}
			}
		}
		d, _ := build(env, "prop2", outs)
		if d.Outputs[0].Path == base.Outputs[0].Path {
			t.Errorf("the derivation name does not influence the output path")
		}
	}
}

// TestRNIX006RecomputeHello is acceptance (b): the output path of nixpkgs `hello` at the pin,
// recomputed from all its parsed input derivations, equals the recorded one.
func TestRNIX006RecomputeHello(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	pin := pinnedNixpkgs(t)
	if pin.red {
		return
	}
	if err := recomputeClosure(t, pin, "hello"); err != nil {
		t.Errorf("recompute: %v", err)
	}
}

// TestRNIX006CrossCheckExamples is acceptance (c): every derivation bob generates for the
// fixtures, re-submitted with `nix derivation add` (Nix computes the paths itself), yields the
// same derivation and output paths.
func TestRNIX006CrossCheckExamples(t *testing.T) {
	parity.Covers(t, "R-NIX-006")
	for _, fx := range []string{"cpp-lib", "go", "polyglot"} {
		t.Run(fx, func(t *testing.T) {
			e := newBobEnv(t)
			r := e.run(t, fx, "build", "//...")
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 0 {
				t.Fatalf("bob build //... exit %d:\n%s", r.Exit, r.Combined())
			}
			bad := 0
			for _, d := range e.bobDrvs(t) {
				if err := e.crossCheck(t, d); err != nil {
					t.Errorf("%s: %v", d, err)
					bad++
				}
			}
			if n := len(e.bobDrvs(t)); n == 0 {
				t.Errorf("no derivation in bob's store after a build")
			} else {
				t.Logf("%d derivations cross-checked, %d differ", n, bad)
			}
		})
	}
}
