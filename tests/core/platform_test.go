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
// with the expected output; every derivation of the build has the host Nix system.
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

// R-FW-031: the matrix over the configurations: the number of distinct derivations equals the
// golden, and configurations with different projections of one package never share its path.
func TestRFW031VariantMatrix(t *testing.T) {
	parity.Covers(t, "R-FW-031")
	dir := s7Repo(t)
	home := t.TempDir()
	host, other := string(hostPlatform(t)), string(otherPlatform(t))
	configs := []string{
		"@" + host, "@" + host + "+debug", "@" + host + "+asan", "@" + host + "+asan+ubsan", "@" + host + "+race",
		"@" + other, "@" + other + "+debug", "@" + other + "+asan",
	}
	perConfig := map[string]map[string]bool{}
	for _, c := range configs {
		if r := bobIn(t, dir, home, "build", "//cmd/api:api"+c, "//rdemo:rdemo"+c); harness.Red(t, r) || !exitIs(t, r, 0) {
			return
		}
		all, ok := drvStore(t, home)
		if !ok {
			return
		}
		set := map[string]bool{}
		for n := range bobDrvs(all) {
			set[n] = true
		}
		perConfig[c] = set
	}
	// perConfig holds the cumulative store; the drvs of one configuration are the new ones.
	distinct := len(perConfig[configs[len(configs)-1]])
	golden.Assert(t, "tests/goldens/outputs/cross-drvcount.txt", []byte(fmt.Sprintf("%d\n", distinct)), golden.Reqs("R-FW-031"))
	// No two configurations with different projections of one package yield the same path:
	// the C++ library libfoo differs in platform, profile and asan between these pairs.
	slug := "libs-foo-foo"
	prev := map[string]bool{}
	for _, c := range configs {
		now := map[string]bool{}
		for n := range perConfig[c] {
			if strings.Contains(n, slug) {
				now[n] = true
			}
		}
		fresh := 0
		for n := range now {
			if !prev[n] {
				fresh++
			}
		}
		if c != configs[0] && c != "@"+host+"+race" && fresh == 0 {
			t.Errorf("configuration %s (different projection of %s) produced no new derivation for it", c, slug)
		}
		if c == "@"+host+"+race" && fresh != 0 {
			t.Errorf("+race changes no C++ projection but produced %d new libfoo derivations", fresh)
		}
		prev = now
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
		{"libs-foo-foo", "openssl-", targetSys},
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
	for _, sub := range []string{"libs-mathx-mathx", "libs-pack-pack"} {
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
	for name, d := range all {
		if strings.Contains(fmt.Sprint(d.Env, d.Args, d.Structured), "static.rust-lang.org") {
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

// projection rows tested by R-FW-038: label slug in the derivation name and which fields
// (platform, profile, asan, race) must change it.
var projectionRows = []struct {
	row, slug, pattern string
	changes            [4]bool
}{
	{"C/C++ library", "libs-foo-foo", "//libs/foo:foo", [4]bool{true, true, true, false}},
	{"Rust target crate", "crates-rcore-rcore", "//crates/rcore:rcore", [4]bool{true, true, false, false}},
	{"Rust host unit", "rmac", "//rmac:rmac", [4]bool{false, false, false, false}},
	{"Rust build-script run", "rdemo-build-run", "//rdemo:rdemo", [4]bool{true, true, false, false}},
}

// R-FW-038: flipping a field marked – leaves the derivation of the package unchanged, flipping a
// ✔ field changes it; the computed table is golden. Requesting +asan for a Rust-only target
// prints "note: //rcore:rcore is built without asan (not supported for Rust in v1)".
func TestRFW038ProjectionTable(t *testing.T) {
	parity.Covers(t, "R-FW-038")
	dir := s7Repo(t)
	host, other := string(hostPlatform(t)), string(otherPlatform(t))
	flips := []struct{ name, from, to string }{
		{"platform", "@" + host, "@" + other},
		{"profile", "@" + host, "@" + host + "+debug"},
		{"asan", "@" + host, "@" + host + "+asan"},
		{"race", "@" + host, "@" + host + "+race"},
	}
	var table strings.Builder
	table.WriteString("row\tplatform\tprofile\tasan\trace\n")
	results := map[string][4]string{}
	for fi, f := range flips {
		for _, row := range projectionRows {
			home := t.TempDir()
			names := func(cfg string) ([]string, bool) {
				if r := bobIn(t, dir, home, "build", row.pattern+cfg); harness.Red(t, r) || !exitIs(t, r, 0) {
					return nil, false
				}
				all, ok := drvStore(t, home)
				return drvNamesWith(all, row.slug), ok
			}
			a, ok := names(f.from)
			if !ok {
				return
			}
			// Only the derivations that are new for the flipped configuration count.
			before := map[string]bool{}
			for _, n := range a {
				before[n] = true
			}
			b, ok := names(f.to)
			if !ok {
				return
			}
			changed := false
			for _, n := range b {
				if !before[n] {
					changed = true
				}
			}
			r := results[row.row]
			r[fi] = map[bool]string{true: "✔", false: "–"}[changed]
			results[row.row] = r
			if changed != row.changes[fi] {
				t.Errorf("%s: flipping %s: derivation changed=%v, want %v", row.row, f.name, changed, row.changes[fi])
			}
		}
	}
	for _, row := range projectionRows {
		r := results[row.row]
		fmt.Fprintf(&table, "%s\t%s\t%s\t%s\t%s\n", row.row, r[0], r[1], r[2], r[3])
	}
	golden.Assert(t, "tests/goldens/outputs/projection-table.txt", []byte(table.String()), golden.Reqs("R-FW-038"))
	// The note for a dropped field.
	r := bobRun(t, dir, "build", "//crates/rcore:rcore@"+host+"+asan")
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
	names := drvNamesWith(all, "libs-foo-foo")
	re := regexp.MustCompile(`^[0-9a-z]{32}-libs-foo-foo-` + regexp.QuoteMeta(other) + `-address\.drv$`)
	found := false
	for _, n := range names {
		found = found || re.MatchString(n)
	}
	if !found {
		t.Errorf("no derivation named libs-foo-foo-%s-address among %v", other, names)
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
