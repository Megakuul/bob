package core

// SPEC §1.9 and §04.2–§04.3 platforms, toolchains, variants: R-FW-030 … R-FW-039.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
)

// s7Repo is the polyglot fixture plus the S7 Rust crates rdemo (proc-macro, build script,
// feature shout) and rmac (testdata/repos/s7extra).
func s7Repo(t *testing.T) string {
	t.Helper()
	dir := harness.Fixture(t, "polyglot")
	copyTree(t, filepath.Join(harness.Root(t), "tests", "core", "testdata", "repos", "s7extra"), dir)
	// Ruling G15 (R-FW-031/038): named variants are func(*bob.Variant) that set
	// Variant.Features[bob.Label{Dir, Name}] (a feature list per crate label), SPEC §1.9.
	// Named variants that add Rust features (Variant.Features, SPEC §1.9): "shout" for rdemo
	// (the "feature shout" configurations of S7 §3.3) and "xtra" for the proc-macro rmac.
	replaceIn(t, filepath.Join(dir, "bob.go"), "Variants: []bob.Named{\n",
		"Variants: []bob.Named{\n"+
			"\t\tbob.NamedVariant(\"shout\", func(v *bob.Variant) { v.Features = map[bob.Label][]string{{Dir: \"rdemo\", Name: \"rdemo\"}: {\"shout\"}} }),\n"+
			"\t\tbob.NamedVariant(\"xtra\", func(v *bob.Variant) { v.Features = map[bob.Label][]string{{Dir: \"rmac\", Name: \"rmac\"}: {\"extra\"}} }),\n")
	git(t, dir, "add", "-A")
	git(t, dir, "-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "s7")
	return dir
}

// pinnedTool returns the store path of a nixpkgs tool of the pin (HARNESS.md §1.4).
func pinnedTool(t *testing.T, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	p, err := serverenv.NixTool(ctx, name)
	if err != nil {
		t.Fatalf("%s from the nixpkgs pin: %v", name, err)
	}
	return p
}

// bobIn runs bob with its own bob home and returns the result; the home is shared between
// calls with the same value so derivations accumulate.
func bobIn(t *testing.T, dir, home string, args ...string) harness.Result {
	t.Helper()
	return bobRunEnv(t, dir, []string{"BOB_HOME=" + home}, args...)
}

// drvStore parses every .drv in the store of home: store file name → derivation. ok is false
// when drv.Parse is not implemented (the test is then red).
func drvStore(t *testing.T, home string) (map[string]*drv.Derivation, bool) {
	t.Helper()
	store := filepath.Join(home, "nix", "store")
	ents, err := os.ReadDir(store)
	if err != nil {
		t.Fatalf("bob home has no store: %v", err)
	}
	out := map[string]*drv.Derivation{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".drv") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		d, err := drv.Parse(b)
		if isNotImplemented(err) {
			harness.RedFeature(t, "drv.Parse")
			return nil, false
		}
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		out[e.Name()] = d
	}
	return out, true
}

func bobDrvs(all map[string]*drv.Derivation) map[string]*drv.Derivation {
	out := map[string]*drv.Derivation{}
	for n, d := range all {
		if strings.HasSuffix(d.Builder, "/bin/bob-builder") {
			out[n] = d
		}
	}
	return out
}

// drvNamesWith lists the store names of bob derivations whose name contains sub.
func drvNamesWith(all map[string]*drv.Derivation, sub string) []string {
	var out []string
	for n := range bobDrvs(all) {
		if strings.Contains(n, sub) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// printPaths builds patterns with --print-paths and returns label → first output path.
func printPaths(t *testing.T, dir, home string, args ...string) (map[string]string, bool) {
	t.Helper()
	r := bobIn(t, dir, home, append([]string{"build", "--print-paths", "--json"}, args...)...)
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return nil, false
	}
	out := map[string]string{}
	for _, e := range eventsNamed(events(t, r), "package.finish") {
		if ps, _ := e["paths"].([]any); len(ps) > 0 {
			out[e.str("label")], _ = ps[0].(string)
		}
	}
	return out, true
}

// qemuRun runs the guest binary under qemu-user; home is the bob home whose store holds the
// guest's /nix/store.
func qemuRun(t *testing.T, arch, home, binary string, args ...string) harness.Result {
	t.Helper()
	qemu := filepath.Join(pinnedTool(t, "qemu-user"), "bin", "qemu-"+arch)
	cmd := exec.Command(qemu, append([]string{"-L", home, binary}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TZ=UTC", "LC_ALL=C"}
	return runCmd(t, cmd, append([]string{"qemu-" + arch, binary}, args...))
}

const apiLine = "digest=81b637d8fcd2c6da6359e6963113a1170de795e4b725b84d1e0b4cfd9ec58ce9 numhex=3432 proto_bytes=116"

// R-FW-030: polyglot cross-builds for the other platform and the binary runs under qemu-user
// with the expected output. Ruling G11: derivations of host-side tools (bob-builder, generators)
// have the host Nix system; target libraries and outputs have the target system (R-FW-032), so
// the test checks the host system for the bob-builder derivations only.
func TestRFW030CrossBuildKeepsHostSystem(t *testing.T) {
	parity.Covers(t, "R-FW-030")
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	other := otherPlatform(t)
	paths, ok := printPaths(t, dir, home, "//cmd/api:api@"+string(other))
	if !ok {
		return
	}
	api := paths["//cmd/api:api"]
	if api == "" {
		t.Fatalf("no output path for //cmd/api:api; got %v", paths)
	}
	arch := map[bob.Platform]string{bob.LinuxAMD64: "x86_64", bob.LinuxARM64: "aarch64"}[other]
	r := qemuRun(t, arch, home, filepath.Join(api, "bin", "api"))
	if r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
		t.Errorf("under qemu-%s the binary printed (exit %d) %q, want %q", arch, r.Exit, r.Stdout+r.Stderr, apiLine)
	}
	all, ok := drvStore(t, home)
	if !ok {
		return
	}
	host := nixSystem(hostPlatform(t))
	n := 0
	for name, d := range bobDrvs(all) {
		n++
		if d.System != host {
			t.Errorf("derivation %s has system %q, want the host system %q", name, d.System, host)
		}
	}
	if n == 0 {
		t.Errorf("no bob derivation in the store")
	}
}

// R-FW-030: a kind may restrict platforms through its options; another platform fails with
// "<label> does not support <platform>" (usage error 0006, exit 2).
func TestRFW030PlatformRestriction(t *testing.T) {
	parity.Covers(t, "R-FW-030")
	dir := repo(t, "platrestrict")
	only := string(hostPlatform(t))
	other := string(otherPlatform(t))
	hostConst := map[bob.Platform]string{bob.LinuxARM64: "bob.LinuxARM64", bob.LinuxAMD64: "bob.LinuxAMD64"}[hostPlatform(t)]
	replaceIn(t, filepath.Join(dir, "only", "bob.go"), "[]bob.Platform{bob.LinuxARM64, bob.LinuxAMD64}", "[]bob.Platform{"+hostConst+"}")
	r := bobRun(t, dir, "build", "//only:only@"+other, "--json")
	if harness.Red(t, r) || !exitIs(t, r, 2) {
		return
	}
	e, ok := errorWithCode(t, r, "BOB-E-0006")
	if ok && !strings.HasPrefix(e.Message, "//only:only does not support "+other) {
		t.Errorf("message %q, want it to start with %q", e.Message, "//only:only does not support "+other)
	}
	if r := bobRun(t, dir, "build", "//only:only@"+only); !exitIs(t, r, 0) {
		return
	}
}

// drvBase is the name of a store path without hash prefix and ".drv" suffix.
func drvBase(storeName string) string {
	if len(storeName) < 33 {
		return storeName
	}
	return strings.TrimSuffix(storeName[33:], ".drv")
}

// libfooRe matches the derivation of the C++ library libfoo itself: §01 R-FW-038 names it
// libs-foo-foo[-tags], §03.1.3 cc-foo[-projection]; the source import (cc-src-…) is not it.
var libfooRe = regexp.MustCompile(`^(?:libs-foo-foo|cc-foo)(?:-|$)`)

// matrixDrvCount is the number of distinct derivations of the 11 configurations of S7 §3.3
// (SPEC R-FW-031: "S7: 99, ±0 after the fixture is frozen"; S7 §3.3: 99 instead of 313).
const matrixDrvCount = 99

// R-FW-031: the matrix over the 11 configurations of S7 §3.3 (matrix.sh): arm64 release, debug,
// asan, asan+ubsan, race, feature shout; amd64 release, cross libs, debug, asan, feature shout.
// The number of distinct derivations equals SPEC's 99, and configurations with different
// projections of one package never share its path.
func TestRFW031VariantMatrix(t *testing.T) {
	parity.Covers(t, "R-FW-031")
	dir := s7Repo(t)
	home := t.TempDir()
	host, other := string(hostPlatform(t)), string(otherPlatform(t))
	// libfooNew: whether the configuration changes libfoo's projection relative to every
	// configuration built before it (S7 §3.3 "What is new"): race and feature shout do not.
	type config struct {
		name, suffix string
		libfooNew    bool
		crossLibs    bool
	}
	configs := []config{
		{"arm64 release", "@" + host, true, false},
		{"arm64 debug", "@" + host + "+debug", true, false},
		{"arm64 asan", "@" + host + "+asan", true, false},
		{"arm64 asan+ubsan", "@" + host + "+asan+ubsan", true, false},
		{"arm64 race", "@" + host + "+race", false, false},
		{"arm64 feature shout", "@" + host + "+shout", false, false},
		{"amd64 release", "@" + other, true, false},
		{"amd64 debug", "@" + other + "+debug", true, false},
		{"amd64 asan", "@" + other + "+asan", true, false},
		{"amd64 feature shout", "@" + other + "+shout", false, false},
		{"amd64 cross libs", "@" + other, true, true},
	}
	if len(configs) != 11 {
		t.Fatalf("harness: %d configurations, S7 §3.3 has 11", len(configs))
	}
	prev := map[string]bool{}
	for _, c := range configs {
		if c.crossLibs {
			// The package definitions change, not the request: cc.CrossLibs on libfoo (the
			// cgo/link package follows its dependency), as in R-FW-036.
			replaceIn(t, filepath.Join(dir, "libs", "foo", "bob.go"), "Std:  cc.CXX20,", "Std:  cc.CXX20,\n\t\tLibs: cc.CrossLibs,")
		}
		if r := bobIn(t, dir, home, "build", "//cmd/api:api"+c.suffix, "//rdemo:rdemo"+c.suffix); harness.Red(t, r) || !exitIs(t, r, 0) {
			return
		}
		all, ok := drvStore(t, home)
		if !ok {
			return
		}
		fresh, freshLibfoo := 0, 0
		for n := range bobDrvs(all) {
			if prev[n] {
				continue
			}
			prev[n] = true
			fresh++
			if libfooRe.MatchString(drvBase(n)) {
				freshLibfoo++
			}
		}
		if fresh == 0 {
			t.Errorf("configuration %s produced no new derivation (S7 §3.3: every configuration has a different projection of some package)", c.name)
		}
		if c.libfooNew && freshLibfoo == 0 {
			t.Errorf("configuration %s (different projection of libfoo) produced no new libfoo derivation", c.name)
		}
		if !c.libfooNew && freshLibfoo != 0 {
			t.Errorf("configuration %s changes no libfoo projection but produced %d new libfoo derivations", c.name, freshLibfoo)
		}
	}
	// The distinct derivations of all configurations (the store is shared, so identical
	// projections share one path): SPEC's number, S7: 99.
	if len(prev) != matrixDrvCount {
		t.Errorf("distinct derivations over the 11 configurations: %d, want %d (R-FW-031, S7 §3.3)", len(prev), matrixDrvCount)
	}
}

// R-FW-032 (a): Tool("protobuf") comes from the host package set and Lib("openssl") from the
// target set: for //cmd/api:api@<other> the build's derivations reference the aarch64 protobuf
// and the x86_64 openssl (legacyPackages.<system>.<attr>).
func TestRFW032TypedNixpkgsSets(t *testing.T) {
	parity.Covers(t, "R-FW-032")
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	other := otherPlatform(t)
	if r := bobIn(t, dir, home, "build", "//cmd/api:api@"+string(other)); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	all, ok := drvStore(t, home)
	if !ok {
		return
	}
	hostSys, targetSys := nixSystem(hostPlatform(t)), nixSystem(other)
	sysOf := func(prefix string) map[string]bool {
		out := map[string]bool{}
		for name, d := range all {
			if strings.HasPrefix(name[33:], prefix) && !strings.HasSuffix(d.Builder, "/bin/bob-builder") {
				out[d.System] = true
			}
		}
		return out
	}
	if s := sysOf("protobuf-"); !s[hostSys] || s[targetSys] {
		t.Errorf("protobuf derivations of systems %v: want only the host system %s", s, hostSys)
	}
	if s := sysOf("openssl-"); !s[targetSys] {
		t.Errorf("openssl derivations of systems %v: want the target system %s", s, targetSys)
	}
	// The libfoo derivation depends on the target openssl, the proto generator on the host protobuf.
	for _, c := range []struct{ drvSub, in, sys string }{
		{"cc-foo", "openssl-", targetSys},
		{"greeting", "protobuf-", hostSys},
	} {
		found := false
		for _, n := range drvNamesWith(all, c.drvSub) {
			for _, in := range all[n].InputDrvs {
				ind := all[filepath.Base(in.Path)]
				if ind != nil && strings.HasPrefix(ind.Name, c.in) && ind.System == c.sys {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("no %s derivation has an input %s* of system %s", c.drvSub, c.in, c.sys)
		}
	}
}

// R-FW-032 (b): nixpkgs.Tool in cc.LibraryOpts.Deps fails to compile with a bob-translated
// compile error naming the bob.go position (and the Go message).
func TestRFW032ToolIsNotALinkable(t *testing.T) {
	parity.Covers(t, "R-FW-032")
	dir := repo(t, "typecheck/nixtool")
	r := bobRun(t, dir, "build", "//...")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	if !regexp.MustCompile(`nt/bob\.go:\d+:\d+: cannot use .*nixpkgs\.Tool.* as cc\.Linkable value`).MatchString(r.Stderr) {
		t.Errorf("stderr lacks a translated \"nt/bob.go:<line>:<col>: cannot use … as cc.Linkable value\":\n%s", r.Stderr)
	}
	if strings.Contains(r.Combined(), ".bob/eval") {
		t.Errorf("the message names the evaluator's rewritten file:\n%s", r.Combined())
	}
}

// R-FW-033: toolchain selection: native uses gcc; cross uses <triple>-gcc/-g++/-ar and the Go
// cross environment; two packages of one kind and target share one toolchain derivation.
func TestRFW033ToolchainSelection(t *testing.T) {
	parity.Covers(t, "R-FW-033")
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	other := otherPlatform(t)
	triple := map[bob.Platform]string{bob.LinuxAMD64: "x86_64-unknown-linux-gnu", bob.LinuxARM64: "aarch64-unknown-linux-gnu"}[other]
	goarch := map[bob.Platform]string{bob.LinuxAMD64: "amd64", bob.LinuxARM64: "arm64"}[other]
	for _, c := range []string{"@" + string(hostPlatform(t)), "@" + string(other)} {
		if r := bobIn(t, dir, home, "build", "//cmd/api:api"+c); harness.Red(t, r) || !exitIs(t, r, 0) {
			return
		}
	}
	all, ok := drvStore(t, home)
	if !ok {
		return
	}
	text := func(d *drv.Derivation) string { return fmt.Sprint(d.Env, d.Args, d.Structured) }
	crossGo := false
	for _, d := range bobDrvs(all) {
		s := text(d)
		if strings.Contains(s, "GOARCH="+goarch) || strings.Contains(s, "GOARCH:"+goarch) {
			crossGo = true
			for _, want := range []string{"GOOS=linux", "CC=" + triple + "-gcc", "CXX=" + triple + "-g++", "-extld=" + triple + "-gcc"} {
				if !strings.Contains(s, strings.ReplaceAll(want, "=", ":")) && !strings.Contains(s, want) {
					t.Errorf("a cross Go derivation (%s) lacks %q", d.Name, want)
				}
			}
		}
	}
	if !crossGo {
		t.Errorf("no Go derivation for GOARCH=%s found", goarch)
	}
	var cxxCross bool
	for _, d := range bobDrvs(all) {
		if strings.Contains(text(d), triple+"-g++") {
			cxxCross = true
		}
	}
	if !cxxCross {
		t.Errorf("no C++ derivation uses %s-g++", triple)
	}
	// Two packages of the same kind and target share one toolchain derivation (cc fixture).
	ccdir := harness.Fixture(t, "cc")
	cchome := t.TempDir()
	if r := bobIn(t, ccdir, cchome, "build", "//libs/mathx:mathx", "//libs/pack:pack"); !exitIs(t, r, 0) {
		return
	}
	ccAll, ok := drvStore(t, cchome)
	if !ok {
		return
	}
	var sets []map[string]bool
	for _, sub := range []string{"cc-mathx", "cc-pack"} {
		names := drvNamesWith(ccAll, sub)
		if len(names) == 0 {
			t.Errorf("no derivation for %s", sub)
			continue
		}
		set := map[string]bool{}
		for _, in := range ccAll[names[0]].InputDrvs {
			if ind := ccAll[filepath.Base(in.Path)]; ind != nil && strings.Contains(ind.Name, "gcc-wrapper") {
				set[in.Path] = true
			}
		}
		sets = append(sets, set)
	}
	if len(sets) == 2 {
		shared := 0
		for p := range sets[0] {
			if sets[1][p] {
				shared++
			}
		}
		if shared == 0 {
			t.Errorf("the two cc libraries do not share one gcc-wrapper derivation: %v vs %v", sets[0], sets[1])
		}
	}
}

// R-FW-034: //rdemo:rdemo for the other platform builds and under qemu prints the golden
// line; rust-std-<triple> is built from the manifest of the target rustc, and no derivation
// fetches from static.rust-lang.org.
func TestRFW034RustTargetStd(t *testing.T) {
	parity.Covers(t, "R-FW-034")
	dir := s7Repo(t)
	home := t.TempDir()
	other := otherPlatform(t)
	arch := map[bob.Platform]string{bob.LinuxAMD64: "x86_64", bob.LinuxARM64: "aarch64"}[other]
	triple := arch + "-unknown-linux-gnu"
	paths, ok := printPaths(t, dir, home, "//rdemo:rdemo@"+string(other))
	if !ok {
		return
	}
	bin := paths["//rdemo:rdemo"]
	if bin == "" {
		t.Fatalf("no output path for //rdemo:rdemo: %v", paths)
	}
	r := qemuRun(t, arch, home, filepath.Join(bin, "bin", "rdemo"))
	want := "rdemo target=" + triple + " answer=42 bits=64 x86cfg=" + fmt.Sprint(other == bob.LinuxAMD64)
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != want {
		t.Errorf("under qemu the binary printed (exit %d) %q, want %q", r.Exit, r.Stdout+r.Stderr, want)
	}
	all, ok := drvStore(t, home)
	if !ok {
		return
	}
	// nixpkgs' own rustc-src and bootstrap fetches from static.rust-lang.org are in the closure
	// of legacyPackages.<sys>.rustc.unwrapped, which R-FW-034 prescribes; what must never
	// appear is a bob derivation fetching from there or any upstream rust-std tarball (T10).
	ours := bobDrvs(all)
	for name, d := range all {
		if s := fmt.Sprint(d.Env, d.Args, d.Structured); strings.Contains(s, "static.rust-lang.org") && (ours[name] != nil || strings.Contains(s, "rust-std")) {
			t.Errorf("derivation %s fetches from static.rust-lang.org", name)
		}
	}
	stds := drvNamesWith(all, "rust-std-"+triple)
	if len(stds) != 1 {
		t.Fatalf("want one rust-std-%s derivation, got %v", triple, stds)
	}
	std := all[stds[0]].Outputs[0].Path
	root := filepath.Join(home, "nix", "store", filepath.Base(std))
	manifest := readFile(t, filepath.Join(root, "lib", "rustlib", "manifest-rust-std-"+triple))
	listed := map[string]bool{"lib/rustlib/manifest-rust-std-" + triple: true}
	for _, l := range lines(manifest) {
		if p, ok := strings.CutPrefix(l, "file:"); ok {
			listed[p] = true
		}
	}
	onDisk := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			onDisk[rel] = true
		}
		return nil
	})
	for p := range listed {
		if !onDisk[p] {
			t.Errorf("rust-std lacks %s listed in its manifest", p)
		}
	}
	for p := range onDisk {
		if !listed[p] {
			t.Errorf("rust-std holds %s which is not in its manifest", p)
		}
	}
}

// R-FW-035: the proc-macro rmac and the build-script compilation have the same derivation
// path in every configuration (host units depend only on the host platform and the crate's
// features); the build-script run differs by platform and profile.
func TestRFW035HostUnits(t *testing.T) {
	parity.Covers(t, "R-FW-035")
	dir := s7Repo(t)
	host, other := string(hostPlatform(t)), string(otherPlatform(t))
	cfgs := []string{"@" + host, "@" + other, "@" + host + "+debug", "@" + other + "+debug"}
	units := map[string][]string{} // substring → store names per configuration
	runs := map[string][]string{}
	for _, c := range cfgs {
		home := t.TempDir()
		if r := bobIn(t, dir, home, "build", "//rdemo:rdemo"+c); harness.Red(t, r) || !exitIs(t, r, 0) {
			return
		}
		all, ok := drvStore(t, home)
		if !ok {
			return
		}
		for _, sub := range []string{"rmac", "rdemo-build-script"} {
			units[sub] = append(units[sub], strings.Join(drvNamesWith(all, sub), ","))
		}
		runs["run"] = append(runs["run"], strings.Join(drvNamesWith(all, "rdemo-build-run"), ","))
	}
	for sub, per := range units {
		if per[0] == "" {
			t.Errorf("no derivation named *%s*", sub)
			continue
		}
		for i := 1; i < len(per); i++ {
			if per[i] != per[0] {
				t.Errorf("%s: derivation differs between %s and %s:\n %s\n %s", sub, cfgs[0], cfgs[i], per[0], per[i])
			}
		}
	}
	if r := runs["run"]; len(r) == 4 && (r[0] == r[1] || r[0] == r[2]) {
		t.Errorf("the build-script run must differ by platform and profile: %v", r)
	}
}

// storeRefs returns the names of the /nix/store paths a file mentions.
func storeRefs(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`/nix/store/[0-9a-z]{32}-([A-Za-z0-9._+-]+)`).FindAllSubmatch(b, -1) {
		out[string(m[1])] = true
	}
	return out
}

// R-FW-036 (a): by default the target libraries come from the target system's set: the
// closure holds both glibc-<triple>-… (cross cc) and glibc-… (openssl); with cc.CrossLibs
// only the cross glibc.
func TestRFW036TargetLibrarySource(t *testing.T) {
	parity.Covers(t, "R-FW-036")
	other := otherPlatform(t)
	triple := map[bob.Platform]string{bob.LinuxAMD64: "x86_64-unknown-linux-gnu", bob.LinuxARM64: "aarch64-unknown-linux-gnu"}[other]
	crossGlibc := regexp.MustCompile(`^glibc-` + triple + `-2\.\d+`)
	nativeGlibc := regexp.MustCompile(`^glibc-2\.\d+`)
	has := func(refs map[string]bool, re *regexp.Regexp) bool {
		for n := range refs {
			if re.MatchString(n) {
				return true
			}
		}
		return false
	}
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	paths, ok := printPaths(t, dir, home, "//cmd/api:api@"+string(other))
	if !ok {
		return
	}
	foo := paths["//libs/foo:foo"]
	api := paths["//cmd/api:api"]
	if foo == "" || api == "" {
		t.Fatalf("no output paths for libfoo and the api: %v", paths)
	}
	refs := storeRefs(t, filepath.Join(api, "bin", "api"))
	for n := range storeRefs(t, filepath.Join(foo, "lib", "libfoo.so")) {
		refs[n] = true
	}
	if !has(refs, crossGlibc) || !has(refs, nativeGlibc) {
		t.Errorf("the default closure does not hold both glibc-%s and glibc: %v", triple, keys(refs))
	}
	// With cc.CrossLibs: only the cross glibc (the libs come from pkgsCross: a source build,
	// several minutes; the closure is checked on the result).
	crossDir := harness.Fixture(t, "polyglot")
	replaceIn(t, filepath.Join(crossDir, "libs", "foo", "bob.go"), "Std:  cc.CXX20,", "Std:  cc.CXX20,\n\t\tLibs: cc.CrossLibs,")
	chome := t.TempDir()
	cpaths, ok := printPaths(t, crossDir, chome, "//libs/foo:foo@"+string(other))
	if !ok {
		return
	}
	crefs := storeRefs(t, filepath.Join(cpaths["//libs/foo:foo"], "lib", "libfoo.so"))
	if !has(crefs, crossGlibc) || has(crefs, nativeGlibc) {
		t.Errorf("with cc.CrossLibs the closure must hold only the cross glibc: %v", keys(crefs))
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// R-FW-036 (b): with an uncached target library the CLI prints the note before any build
// (JSON event with code uncached-target-lib, attr and platform). pkgsCross openssl is not in
// the binary cache (S7: 369 s from source), so bob is stopped as soon as the note appears.
func TestRFW036UncachedTargetLibNote(t *testing.T) {
	parity.Covers(t, "R-FW-036")
	other := string(otherPlatform(t))
	dir := harness.Fixture(t, "polyglot")
	replaceIn(t, filepath.Join(dir, "libs", "foo", "bob.go"), "Std:  cc.CXX20,", "Std:  cc.CXX20,\n\t\tLibs: cc.CrossLibs,")
	for _, jsonMode := range []bool{true, false} {
		args := []string{"build", "//libs/foo:foo@" + other}
		if jsonMode {
			args = append(args, "--json")
		}
		cmd := exec.Command(harness.Bin(t, "bob"), args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "BOB_HOME="+t.TempDir(), "TZ=UTC", "LC_ALL=C")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("harness: %v", err)
		}
		deadline := time.AfterFunc(4*time.Minute, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
		sc := bufio.NewScanner(stdout)
		var seen []string
		noted, buildStarted := false, false
		for sc.Scan() {
			line := sc.Text()
			seen = append(seen, line)
			if jsonMode {
				var e map[string]any
				if json.Unmarshal([]byte(line), &e) == nil {
					if e["code"] == "uncached-target-lib" && (e["type"] == "note" || e["event"] == "note") {
						noted = true
						if e["attr"] != "openssl" || e["platform"] != other {
							t.Errorf("note event %v, want attr openssl and platform %s", e, other)
						}
					}
					if e["event"] == "package.start" && e["action"] == "build" && !noted {
						buildStarted = true
					}
				}
			} else if strings.Contains(line, "note: openssl for "+other+" is not in the binary cache and will be built from source (several minutes)") {
				noted = true
			}
			if noted || buildStarted {
				break
			}
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		deadline.Stop()
		out := strings.Join(seen, "\n") + stderr.String()
		if strings.Contains(out, harness.NotImplemented) {
			harness.RedFeature(t, "bob build (uncached target library note)")
			return
		}
		if jsonMode && buildStarted {
			t.Errorf("a build started before the uncached-target-lib note")
		}
		if !noted && !(!jsonMode && strings.Contains(stderr.String(), "note: openssl for "+other+" is not in the binary cache and will be built from source (several minutes)")) {
			t.Errorf("json=%v: no uncached-target-lib note before the build:\n%s", jsonMode, out)
		}
	}
}

// R-FW-037: the cross sanitiser runtime path: readelf -d of //cmd/api:api@<other>+asan lists
// the cross gcc-lib directory in RUNPATH. (Starting the binary on a native worker without
// LD_LIBRARY_PATH needs amd64 hardware and is (manual) on this host.)
func TestRFW037CrossSanitiserRunpath(t *testing.T) {
	parity.Covers(t, "R-FW-037")
	other := otherPlatform(t)
	triple := map[bob.Platform]string{bob.LinuxAMD64: "x86_64-unknown-linux-gnu", bob.LinuxARM64: "aarch64-unknown-linux-gnu"}[other]
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	paths, ok := printPaths(t, dir, home, "//cmd/api:api@"+string(other)+"+asan")
	if !ok {
		return
	}
	api := paths["//cmd/api:api"]
	if api == "" {
		t.Fatalf("no output path for //cmd/api:api: %v", paths)
	}
	readelf := filepath.Join(pinnedTool(t, "binutils"), "bin", "readelf")
	r := runCmd(t, exec.Command(readelf, "-d", filepath.Join(api, "bin", "api")), []string{"readelf", "-d", "api"})
	if !exitIs(t, r, 0) {
		return
	}
	re := regexp.MustCompile(`(?m)\((?:RUNPATH|RPATH)\).*[:\[][^\n]*/nix/store/[0-9a-z]{32}-` + regexp.QuoteMeta(triple) + `-gcc-[0-9.]+-lib/lib`)
	if !re.MatchString(r.Stdout) {
		t.Errorf("RUNPATH does not list the cross gcc-lib directory:\n%s", r.Stdout)
	}
}

// rustUnitRe matches the store name of a local Rust crate unit (§03.3.3: units are named
// <name>-<version>[-host][-<projection>], build scripts <name>-<version>[-host]-build-script
// and -build-run). Ruling G14: §03 per-ecosystem names win for ecosystem packages, §01's generic
// scheme (an optional <dir>- prefix) is the fallback; a local crate's version in names is
// always 0.0.0 (the "local" form is dropped).
func rustUnitRe(name, kind string) *regexp.Regexp {
	pre := `^(?:[a-z0-9_.]+-)?` + regexp.QuoteMeta(name) + `-0\.0\.0`
	switch kind {
	case "host":
		return regexp.MustCompile(pre + `-host(?:-[a-z0-9-]+)?$`)
	case "run":
		return regexp.MustCompile(pre + `(?:-host)?-build-run(?:-[a-z0-9-]+)?$`)
	}
	return regexp.MustCompile(pre + `(?:-[a-z0-9-]+)?$`)
}

// projFlips are the variant fields flipped by R-FW-038, in the column order of the table:
// platform, profile, ASan, UBSan, Go race, Rust features. The last flip's token is the row's own
// feature variant (shout for rdemo, xtra for rmac); a row without Rust features flips shout.
var projFlips = []string{"platform", "profile", "asan", "ubsan", "race", "features"}

// projectionRow is one row of the §04 R-FW-038 table: a pattern whose package kind the row
// describes, the regexp of the derivations of that kind (store name without hash and .drv) and
// which flips must change them (✔) or not (–), column order of projFlips.
type projectionRow struct {
	row, fixture, pattern string
	re                    *regexp.Regexp
	changes               [6]bool
	featureTok            string // variant token of the features flip
	all                   bool   // match every derivation of the store (nixpkgs, sources), not only bob's
}

// The 13 rows of the table (the 14th, OCI/AppImage/tarball/release, is the union of their
// roots' projections and has no field of its own). Names follow §03 per ecosystem: fetch
// crate-<name>-<version> (§03.3.3), codegen gen-<label slug> (§03.6.3), Go go-std…,
// go-<module>-<version>, go-<importpath>, go-link-<name> (§03.2.3), npm app/site <label>
// (§03.4.3), OCaml ml-<name> (§03.5.3), C/C++ libs-foo-foo (§01 R-FW-038) or cc-foo (§03.1.3).
var projectionRows = []projectionRow{
	{row: "fetch / unpack", fixture: "s7", pattern: "//rdemo:rdemo", re: regexp.MustCompile(`^crate-itoa-`), all: true},
	{row: "codegen (host tool)", fixture: "s7", pattern: "//cmd/api:api", re: regexp.MustCompile(`^gen-`), all: true},
	{row: "nixpkgs.Tool", fixture: "s7", pattern: "//cmd/api:api", re: regexp.MustCompile(`^protobuf-`), all: true},
	{row: "nixpkgs.Lib", fixture: "s7", pattern: "//libs/foo:foo", re: regexp.MustCompile(`^openssl-`), all: true,
		changes: [6]bool{true, false, false, false, false, false}},
	{row: "C/C++ object/library/binary", fixture: "s7", pattern: "//libs/foo:foo", re: libfooRe,
		changes: [6]bool{true, true, true, true, false, false}},
	{row: "Go std, Go package (pure)", fixture: "s7", pattern: "//cmd/api:api", re: regexp.MustCompile(`^go-github\.com-google-uuid-`),
		changes: [6]bool{true, true, true, false, true, false}},
	{row: "Go cgo package, Go link", fixture: "s7", pattern: "//cmd/api:api", re: regexp.MustCompile(`^(?:go-link-api|go-example\.com-polyglot-cmd-api)(?:-|$)`),
		changes: [6]bool{true, true, true, true, true, false}},
	{row: "Rust target crate", fixture: "s7", pattern: "//rdemo:rdemo", re: rustUnitRe("rdemo", "compile"),
		changes: [6]bool{true, true, false, false, false, true}, featureTok: "shout"},
	{row: "Rust host unit", fixture: "s7", pattern: "//rmac:rmac", re: rustUnitRe("rmac", "host"),
		changes: [6]bool{false, false, false, false, false, true}, featureTok: "xtra"},
	{row: "Rust build-script run", fixture: "s7", pattern: "//rdemo:rdemo", re: rustUnitRe("rdemo", "run"),
		changes: [6]bool{true, true, false, false, false, true}, featureTok: "shout"},
	{row: "npm/TS compile, bundle", fixture: "s7", pattern: "//web:web", re: regexp.MustCompile(`^web-web(?:-|$)`),
		changes: [6]bool{false, true, false, false, false, false}},
	{row: "npm app runtime (nodejs Lib)", fixture: "npm", pattern: "//native:native", re: regexp.MustCompile(`^nodejs-`), all: true,
		changes: [6]bool{true, false, false, false, false, false}},
	{row: "OCaml", fixture: "ocaml", pattern: "//app:demo", re: regexp.MustCompile(`^ml-demo(?:-|$)`),
		changes: [6]bool{false, true, false, false, false, false}},
}

// R-FW-038: for each of the 13 rows, flipping a field marked – leaves the .drv path of the
// package kind unchanged, flipping a ✔ field changes it; the computed table is golden.
// Requesting +asan for a Rust-only target prints
// "note: //crates/rcore:rcore is built without asan (not supported for Rust in v1)".
func TestRFW038ProjectionTable(t *testing.T) {
	parity.Covers(t, "R-FW-038")
	if len(projectionRows) != 13 {
		t.Fatalf("harness: %d rows, the R-FW-038 table has 13 (plus the union row of OCI/AppImage/tarball/release)", len(projectionRows))
	}
	s7 := s7Repo(t)
	host, other := string(hostPlatform(t)), string(otherPlatform(t))
	var table strings.Builder
	table.WriteString("row\tplatform\tprofile\tasan\tubsan\trace\tfeatures\n")
	for _, row := range projectionRows {
		dir := s7
		switch row.fixture {
		case "npm", "ocaml":
			dir = harness.Fixture(t, row.fixture)
		}
		// One store per row: a flipped configuration's derivations are "new" when no earlier
		// configuration of this row produced them (every flip differs from the base by one field).
		home := t.TempDir()
		seen := map[string]bool{}
		build := func(cfg string) (map[string]bool, bool) {
			if r := bobIn(t, dir, home, "build", row.pattern+cfg); harness.Red(t, r) || !exitIs(t, r, 0) {
				return nil, false
			}
			all, ok := drvStore(t, home)
			if !ok {
				return nil, false
			}
			fresh := map[string]bool{}
			for n, d := range all {
				if !row.all && !strings.HasSuffix(d.Builder, "/bin/bob-builder") {
					continue
				}
				if row.re.MatchString(drvBase(n)) && !seen[n] {
					fresh[n] = true
					seen[n] = true
				}
			}
			return fresh, true
		}
		base, ok := build("@" + host)
		if !ok {
			return
		}
		if len(base) == 0 {
			t.Errorf("%s: no derivation named %v among the store after building %s@%s", row.row, row.re, row.pattern, host)
			continue
		}
		toks := []string{"@" + other, "@" + host + "+debug", "@" + host + "+asan", "@" + host + "+ubsan", "@" + host + "+race", ""}
		toks[5] = "@" + host + "+shout"
		if row.featureTok != "" {
			toks[5] = "@" + host + "+" + row.featureTok
		}
		var cells [6]string
		for fi, cfg := range toks {
			fresh, ok := build(cfg)
			if !ok {
				return
			}
			changed := len(fresh) > 0
			cells[fi] = map[bool]string{true: "✔", false: "–"}[changed]
			if changed != row.changes[fi] {
				t.Errorf("%s: flipping %s (%s): derivation changed=%v, want %v", row.row, projFlips[fi], cfg, changed, row.changes[fi])
			}
		}
		fmt.Fprintf(&table, "%s\t%s\n", row.row, strings.Join(cells[:], "\t"))
	}
	golden.Assert(t, "tests/goldens/outputs/projection-table.txt", []byte(table.String()), golden.Reqs("R-FW-038"))
	// The note for a dropped field.
	r := bobRun(t, s7, "build", "//crates/rcore:rcore@"+host+"+asan")
	if !exitIs(t, r, 0) {
		return
	}
	if !strings.Contains(r.Stderr+r.Stdout, "note: //crates/rcore:rcore is built without asan (not supported for Rust in v1)") {
		t.Errorf("no note about the dropped asan field:\n%s%s", r.Stdout, r.Stderr)
	}
}

// R-FW-038: derivation names: <dir-label>-<name>[-host][-<platform if cross>][-<tag>], tags in
// canonical order, at most 200 bytes; longer names are cut and end in -<hash8>.
func TestRFW038DerivationNames(t *testing.T) {
	parity.Covers(t, "R-FW-038")
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	other := string(otherPlatform(t))
	if r := bobIn(t, dir, home, "build", "//libs/foo:foo@"+other+"+asan"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	all, ok := drvStore(t, home)
	if !ok {
		return
	}
	names := drvNamesWith(all, "cc-foo")
	// polyglot's named variant asan is ASan + UBSan + Debug (fixture bob.go), so the tag is
	// dbg-address-undefined in R-FW-038's canonical order (T11).
	re := regexp.MustCompile(`^[0-9a-z]{32}-cc-foo-` + regexp.QuoteMeta(other) + `-dbg-address-undefined\.drv$`)
	found := false
	for _, n := range names {
		found = found || re.MatchString(n)
	}
	if !found {
		t.Errorf("no derivation named cc-foo-%s-dbg-address-undefined among %v", other, names)
	}
	for n, d := range bobDrvs(all) {
		if len(d.Name) > 200 {
			t.Errorf("derivation name %s is %d bytes, want ≤ 200", n, len(d.Name))
		}
	}
}

// R-FW-039: a test for the other platform runs under qemu locally (event test.result with
// runner qemu-<arch>); with a sanitiser it is skipped with the native-machine text, exit 0,
// and exit 1 with --require-all-tests.
func TestRFW039ForeignArchitectureTests(t *testing.T) {
	parity.Covers(t, "R-FW-039")
	dir := harness.Fixture(t, "polyglot")
	home := t.TempDir()
	other := otherPlatform(t)
	arch := map[bob.Platform]string{bob.LinuxAMD64: "x86_64", bob.LinuxARM64: "aarch64"}[other]
	r := bobIn(t, dir, home, "test", "//cmd/api:go_test@"+string(other), "--json")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	ran := false
	for _, e := range eventsNamed(events(t, r), "test.result") {
		ran = true
		if e.str("status") != "pass" || e.str("runner") != "qemu-"+arch {
			t.Errorf("%v: want status pass and runner qemu-%s", e, arch)
		}
	}
	if !ran {
		t.Errorf("no test.result event:\n%s", r.Stdout)
	}
	const skipText = "not run: needs a native " + "linux-amd64" + " machine"
	for _, variant := range []string{"+asan", "+race"} {
		label := "//cmd/api:go_test@" + string(other) + variant
		sk := bobIn(t, dir, home, "test", label, "--json")
		if !exitIs(t, sk, 0) {
			return
		}
		var skipped bool
		for _, e := range eventsNamed(events(t, sk), "test.result") {
			skipped = skipped || e.str("status") == "skipped"
		}
		if !skipped {
			t.Errorf("%s: no test.result with status skipped:\n%s", label, sk.Stdout)
		}
		if other == bob.LinuxAMD64 {
			human := bobIn(t, dir, home, "test", label)
			if !strings.Contains(human.Stdout+human.Stderr, skipText) {
				t.Errorf("%s: output lacks %q:\n%s%s", label, skipText, human.Stdout, human.Stderr)
			}
		}
		if req := bobIn(t, dir, home, "test", label, "--require-all-tests"); req.Exit != 1 {
			t.Errorf("%s --require-all-tests exited %d, want 1", label, req.Exit)
		}
	}
}
