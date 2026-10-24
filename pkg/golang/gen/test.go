package gen

import (
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
)

// testBuild is where a test job keeps its intermediate archives (the build directory: they
// are not part of the output).
const testBuild = buildTop + "/gotest"

// defaultTestTimeoutS is the timeout of a test unit that sets none (A92: 10 minutes).
const defaultTestTimeoutS = 600

// planTest plans one test unit: the test binary (go-testmain: the package with its _test.go
// files, the external test package, bob's _testmain.go, the link) and the test-run job.
func (pl *planner) planTest(n *ir.Node) error {
	var s gospec.Test
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return internal(n, "test spec: %v", err)
	}
	t, err := pl.newTarget(n, s.Build)
	if err != nil {
		return err
	}
	ip := importPath(t.mod, s.Dir)
	cl, err := pl.loadTarget(t, []string{ip}, map[string]bool{ip: true})
	if err != nil {
		return err
	}
	under := cl.pkgs[ip]
	bp := under.bp
	tf, err := scanTests(under.dir, bp.TestGoFiles, bp.XTestGoFiles)
	if err != nil {
		return pl.loadError(ip, "", err)
	}
	tf.ImportPath, tf.ModulePath = ip, t.mod.spec.Path
	main, err := testmain(tf)
	if err != nil {
		return internal(n, "test main: %v", err)
	}

	// The packages the test binary links, except the package under test (compiled with its
	// tests inside the test job); kept when another package of the closure imports it.
	deps := &closure{ctx: cl.ctx, pkgs: map[string]*pkg{}}
	for q, p := range cl.pkgs {
		if q != ip || importedByOthers(cl, ip) {
			deps.pkgs[q] = p
		}
	}
	res, err := pl.compileClosure(t, deps, nil)
	if err != nil {
		return err
	}
	if _, ok := deps.pkgs[ip]; !ok {
		// The package under test still needs its embeds resolved for its own compile.
		es, err := resolveEmbeds(under.dir, sortedUnique(bp.EmbedPatterns))
		if err != nil {
			return pl.loadError(ip, "", err)
		}
		under.embeds = es
	}
	// The test tree: the package's files plus its tests and their embeds.
	testEmbeds, err := resolveEmbeds(under.dir, sortedUnique(append(append(slices.Clone(bp.EmbedPatterns), bp.TestEmbedPatterns...), bp.XTestEmbedPatterns...)))
	if err != nil {
		return pl.loadError(ip, "", err)
	}
	saved := under.embeds
	under.embeds = testEmbeds
	srcPaths, err := pl.importSources(&closure{ctx: cl.ctx, pkgs: map[string]*pkg{ip: under}}, map[string]bool{ip: true})
	under.embeds = saved
	if err != nil {
		return err
	}
	src := srcPaths[ip]

	in := newInputs(pl.req.Builder)
	var cc *engine.Resolved
	if cl.ctx.cgo {
		cc = t.cc
	}
	tca := pl.toolchainArgsFor(cl.ctx, cc, in, jobLink)
	if under.cgo() {
		tca.CFlags = cl.ctx.v.cflags()
	}
	tca.CFlags = append(tca.CFlags, t.sanitiserRunpath(cl.ctx, in)...)
	tca.GCFlags = cl.ctx.v.gcflags()
	in.addDrv(res.stdD, res.stdDrv)
	if under.ref.kind == refWorkspace {
		in.srcs[src] = true
	} else {
		pl.addStoreInput(under, in)
	}
	archiveOf := func(q string) (string, error) {
		g := res.byPkg[q]
		if g == nil {
			return "", internal(n, "%s has no compiled archive", q)
		}
		in.addDrv(g.d, g.drv)
		return archive(g.out, q), nil
	}
	// importOf is what a compile of the test job reads for q: its archive, or in the cutoff
	// mode its export data by content (the link still takes the archive).
	importOf := func(q string) (string, error) {
		g := res.byPkg[q]
		if g == nil {
			return "", internal(n, "%s has no compiled archive", q)
		}
		return res.compileImport(g, q, in), nil
	}
	internalArchive := testBuild + "/" + ip + ".a"
	lang := pl.lang(under, t.mod)

	// 1. The package with its _test.go files.
	ptest := pkgJob{ImportPath: ip, Name: bp.Name, Flags: compileFlags(ip, lang), Dir: src,
		GoFiles: sortedUnique(append(slices.Clone(bp.GoFiles), bp.TestGoFiles...)), CgoFiles: bp.CgoFiles,
		CFiles: bp.CFiles, SFiles: bp.SFiles, SysoFiles: bp.SysoFiles, Imports: map[string]string{},
		Trim: trim(under), Out: internalArchive}
	// A main package under test is compiled as a library (its import path, not "main"), as
	// the go command does: the test main is the program.
	ptest.Embed = embedOf(testEmbeds, append(slices.Clone(bp.EmbedPatterns), bp.TestEmbedPatterns...))
	if under.cgo() {
		cf, lf, err := pl.cgoFlags(t, under, in)
		if err != nil {
			return err
		}
		ptest.CFlags, ptest.LDFlags = cf, lf
	}
	for _, imp := range sortedUnique(append(slices.Clone(bp.Imports), bp.TestImports...)) {
		if _, ok := deps.pkgs[imp]; !ok || imp == ip {
			continue
		}
		a, err := importOf(imp)
		if err != nil {
			return err
		}
		ptest.Imports[imp] = a
	}
	pkgs := []pkgJob{ptest}
	link := map[string]string{}
	for _, q := range deps.transitive(sortedUnique(append(append(slices.Clone(bp.Imports), bp.TestImports...), bp.XTestImports...))...) {
		if q == ip {
			continue
		}
		a, err := archiveOf(q)
		if err != nil {
			return err
		}
		link[q] = a
	}
	link[ip] = internalArchive

	// 2. The external test package (package <name>_test).
	mainImports := map[string]string{}
	if tf.ImportTest {
		mainImports[ip] = internalArchive
	}
	if len(bp.XTestGoFiles) > 0 {
		xip := ip + "_test"
		xarchive := testBuild + "/" + xip + ".a"
		x := pkgJob{ImportPath: xip, Name: bp.Name + "_test", Flags: compileFlags(xip, lang), Dir: src,
			GoFiles: sortedUnique(bp.XTestGoFiles), Imports: map[string]string{}, Trim: trim(under), Out: xarchive}
		x.Embed = embedOf(testEmbeds, bp.XTestEmbedPatterns)
		for _, imp := range sortedUnique(bp.XTestImports) {
			if imp == ip {
				x.Imports[imp] = internalArchive
				continue
			}
			if _, ok := deps.pkgs[imp]; !ok {
				continue
			}
			a, err := importOf(imp)
			if err != nil {
				return err
			}
			x.Imports[imp] = a
		}
		pkgs = append(pkgs, x)
		link[xip] = xarchive
		if tf.ImportXtest {
			mainImports[xip] = xarchive
		}
	}

	// 3. bob's test main, then the link.
	mainArchive := testBuild + "/testmain.a"
	pkgs = append(pkgs, pkgJob{ImportPath: "testmain", Name: "main", Flags: compileFlags("main", lang),
		Write: map[string]string{"_testmain.go": main}, GoFiles: []string{"_testmain.go"}, Imports: mainImports,
		Trim: "_testmain", Out: mainArchive})
	pack := ""
	if under.cgo() || len(bp.SFiles)+len(bp.SysoFiles) > 0 {
		if res.packD == nil {
			if res.packDrv, res.packD, err = pl.tools(cl.ctx, t.system); err != nil {
				return internal(n, "%v", err)
			}
			res.pack = outPath(res.packD) + "/bin/pack"
		}
		in.addDrv(res.packD, res.packDrv)
		pack = res.pack
	}
	binName := path.Base(ip) + ".test"
	out := drv.Placeholder("out")
	args := tca.with("std", res.std, "pack", pack, "packages", pkgs,
		"link", map[string]any{"main": mainArchive, "packages": link, "out": out + "/bin/" + binName, "mode": gospec.ModeExe,
			"x": []string{}, "modinfo": pl.modinfo(cl, ip+".test", t, gospec.ModeExe), "external": res.cgo || under.cgo()})
	testDrv, testD, err := pl.job(drv.Name(append([]string{"go-test", ip}, nameParts(cl.ctx.target, cl.ctx.v.tag(cl.ctx.cgo))...)...),
		t.system, "go-testmain", n.Label, args, in)
	if err != nil {
		return internal(n, "%v", err)
	}

	run, err := pl.testRun(n, s, ip, testD, testDrv, binName, cl.ctx)
	if err != nil {
		return err
	}
	pl.out.Nodes[n.ID] = run
	return nil
}

// importedByOthers reports whether a package of cl other than ip imports ip.
func importedByOthers(cl *closure, ip string) bool {
	for q, p := range cl.pkgs {
		if q != ip && slices.Contains(p.imports, ip) {
			return true
		}
	}
	return false
}

// embedOf is the embed config of patterns (resolved in es).
func embedOf(es *embedSet, patterns []string) *embedJob {
	if es == nil || len(patterns) == 0 {
		return nil
	}
	pats := map[string][]string{}
	files := map[string]bool{}
	for _, p := range sortedUnique(patterns) {
		pats[p] = es.patterns[p]
		for _, f := range pats[p] {
			files[f] = true
		}
	}
	return &embedJob{Patterns: pats, Files: sortedKeys(files)}
}

// testRunArgs is bspec.TestRunArgs (generators write the JSON themselves, R-ARCH-003).
type testRunArgs struct {
	Argv     []string          `json:"argv"`
	Path     []string          `json:"path,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Format   string            `json:"format,omitempty"`
	Services []serviceJob      `json:"services,omitempty"`
	TimeoutS int64             `json:"timeoutSeconds,omitempty"`
}

type serviceJob struct {
	Name      string            `json:"name"`
	Argv      []string          `json:"argv"`
	Path      []string          `json:"path,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Ready     probeJob          `json:"ready"`
	TimeoutMs int64             `json:"timeoutMs"`
}

type probeJob struct {
	Port int      `json:"port,omitempty"`
	Argv []string `json:"argv,omitempty"`
}

// testRun plans the test-run job of a unit: run the test binary once with -test.v (parsed
// into cases), its services started first (R-FW-025).
func (pl *planner) testRun(n *ir.Node, s gospec.Test, ip string, testD *drv.Derivation, testDrv, binName string, c buildCtx) (engine.Resolved, error) {
	in := newInputs(pl.req.Builder)
	in.addDrv(testD, testDrv)
	timeout := s.TimeoutS
	if timeout <= 0 {
		timeout = defaultTestTimeoutS
	}
	argv := []string{outPath(testD) + "/bin/" + binName, "-test.v=true", "-test.paniconexit0",
		"-test.timeout=" + strconv.FormatInt(timeout, 10) + "s"}
	argv = append(argv, s.Args...)
	argv = append(argv, pl.req.TestArgs...)
	if c.target.Cross {
		// R-FW-039: a test for another platform runs under the emulator (qemu-user).
		if s.Emulator <= 0 || s.Emulator >= len(n.Deps) {
			return engine.Resolved{}, internal(n, "a test for %s/%s has no emulator", c.goos, c.goarch)
		}
		rv, ok := pl.req.Inputs[n.Deps[s.Emulator]]
		if !ok {
			return engine.Resolved{}, internal(n, "its emulator is not planned")
		}
		q := in.add(rv, pickOutput(rv.Outputs, "out"))
		argv = append([]string{q + "/bin/qemu-" + strings.TrimSuffix(c.target.Platform.NixSystem(), "-linux")}, argv...)
	}
	a := testRunArgs{Argv: argv, Format: "go", TimeoutS: timeout}
	for _, sv := range s.Services {
		job := serviceJob{Name: sv.Name, Env: sv.Env, TimeoutMs: sv.TimeoutMs,
			Ready: probeJob{Port: sv.ReadyPort, Argv: sv.ReadyCmd}}
		var first string
		for _, ti := range sv.Tools {
			if ti < 0 || ti >= len(n.Deps) {
				return engine.Resolved{}, internal(n, "service tool out of range")
			}
			rv, ok := pl.req.Inputs[n.Deps[ti]]
			if !ok {
				return engine.Resolved{}, internal(n, "service tool %s is not planned", n.Deps[ti])
			}
			o := pickOutput(rv.Outputs, "bin", "out")
			root := in.add(rv, o)
			if first == "" {
				first = root
			}
			job.Path = append(job.Path, root+"/bin")
		}
		job.Argv = slices.Clone(sv.Start)
		// A program named relative to the tool's output ("bin/sleeper", bob.ServiceFrom) runs
		// from that output.
		if len(job.Argv) > 0 && strings.Contains(job.Argv[0], "/") && !path.IsAbs(job.Argv[0]) && first != "" {
			job.Argv[0] = first + "/" + job.Argv[0]
		}
		a.Services = append(a.Services, job)
	}
	p, d, err := pl.job(drv.Name(append([]string{"go-test", ip, "run"}, nameParts(c.target, c.v.tag(c.cgo))...)...),
		testD.System, "test-run", n.Label, a, in)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	return engine.Resolved{Drv: p, Outputs: map[string]string{"out": outPath(d)}}, nil
}
