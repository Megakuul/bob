package gen

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
)

// work is the importer's copy inside the build directory.
const work = buildTop + "/w"

// workspace returns the steps that lay the importer out in the build directory: its sources,
// node_modules as a writable directory of symlinks (Vite writes below it, S4-5) and the
// generated package.json and tsconfig.json.
func (pl *planner) workspace(n *ir.Node, im *importer, pi *platInfo, srcIDs []string, in *inputs) ([]step, string, error) {
	var steps []step
	for _, id := range srcIDs {
		src, ok := pl.req.Sources[id]
		if !ok {
			return nil, "", internal(n, "the sources of %s are not imported", id)
		}
		in.srcs[src] = true
		from := src
		if im.dir != "." {
			from = src + "/" + im.dir
		}
		steps = append(steps, step{Op: "copy", From: from, Path: work})
	}
	nm, err := pl.nodeModules(n, im, pi)
	if err != nil {
		return nil, "", err
	}
	in.addDrv(nm.drv)
	steps = append(steps, step{Op: "copy", From: nm.out, Path: work + "/node_modules"})
	md, err := importerDeps(im, pl.byID, pl.sec)
	if err != nil {
		return nil, "", internal(n, "%v", err)
	}
	pj, err := packageJSON(im, md)
	if err != nil {
		return nil, "", err
	}
	tc, err := tsconfigJSON(im)
	if err != nil {
		return nil, "", err
	}
	steps = append(steps, step{Op: "write", Path: work + "/package.json", Content: string(pj)},
		step{Op: "write", Path: work + "/tsconfig.json", Content: string(tc)})
	return steps, nm.out, nil
}

// binScript returns the path below the work directory of command cmd of the importer's direct
// dependency alias (its "bin" entry in bob.lock).
func (pl *planner) binScript(n *ir.Node, im *importer, alias, cmd, purpose string) (string, error) {
	rec := pl.sec.Importers[im.dir]
	key, ok := rec.Deps[alias]
	if !ok {
		return "", invalid(n, "dependencies", fmt.Sprintf("%s needs %s: add npm.Dep(%q, \"<version>\", npm.Dev())", purpose, alias, alias))
	}
	rel, ok := pl.sec.Packages[key].Bin[cmd]
	if !ok {
		return "", internal(n, "%s has no command %s", key, cmd)
	}
	return work + "/node_modules/" + alias + "/" + rel, nil
}

// buildEnv is the PATH of build commands: node, bash and the importer's .bin.
func (pl *planner) buildEnv(n *ir.Node, im *importer, in *inputs) (node string, dirs []string, err error) {
	nodeOut, err := pl.tool(n, im.spec.Tools.Node, in)
	if err != nil {
		return "", nil, err
	}
	bash, err := pl.tool(n, im.spec.Tools.Bash, in)
	if err != nil {
		return "", nil, err
	}
	return nodeOut + "/bin/node", []string{work + "/node_modules/.bin", nodeOut + "/bin", bash + "/bin"}, nil
}

// libEntry is where tsc writes the library's entry: "src/index.ts" → dist/index.js and
// dist/index.d.ts (rootDir src).
func libEntry(entry string) (js, dts string) {
	rel := strings.TrimPrefix(path.Clean(entry), "src/")
	rel = strings.TrimSuffix(rel, path.Ext(rel))
	return "./dist/" + rel + ".js", "./dist/" + rel + ".d.ts"
}

// runtimeLinks are the symlinks a package output needs for its runtime dependencies (Dev()
// dependencies excluded; a library's peers are included, resolved for its own build).
func (pl *planner) runtimeLinks(n *ir.Node, im *importer, pi *platInfo, nmDir string, in *inputs) ([]step, error) {
	deps, dev, err := pl.direct(n, im, pi)
	if err != nil {
		return nil, err
	}
	peers := map[string]bool{}
	for _, p := range im.spec.Peers {
		peers[p.Alias] = true
	}
	s, err := pl.scriptsOf(n, im)
	if err != nil {
		return nil, err
	}
	var steps []step
	for _, alias := range sortedKeys(deps) {
		if dev[alias] && !peers[alias] {
			continue
		}
		dir, err := pl.depDir(n, pi, s, deps[alias], in)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step{Op: "symlink", Path: nmDir + "/" + alias, Target: dir})
	}
	return steps, nil
}

// planLib compiles the library with tsc (R-NPM-006): $out/node_modules/<name> holds its
// package.json and dist/, its runtime dependencies are linked next to it.
func (pl *planner) planLib(n *ir.Node, im *importer) (engine.Resolved, error) {
	pi, err := pl.platformOf(n)
	if err != nil {
		return engine.Resolved{}, err
	}
	in := pl.newInputs()
	steps, _, err := pl.workspace(n, im, pi, []string{n.ID}, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	node, dirs, err := pl.buildEnv(n, im, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	tsc, err := pl.binScript(n, im, "typescript", "tsc", "a library is compiled with tsc, it")
	if err != nil {
		return engine.Resolved{}, err
	}
	out := drv.Placeholder("out")
	pkgDir := out + "/node_modules/" + im.spec.Name
	md, err := importerDeps(im, pl.byID, pl.sec)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	pj, err := packageJSON(im, md)
	if err != nil {
		return engine.Resolved{}, err
	}
	steps = append(steps,
		step{Op: "exec", Path: work, Argv: []string{node, tsc, "-p", "."}, Dirs: dirs},
		step{Op: "copy", From: work + "/dist", Path: pkgDir + "/dist"},
		step{Op: "write", Path: pkgDir + "/package.json", Content: string(pj)})
	links, err := pl.runtimeLinks(n, im, pi, out+"/node_modules", in)
	if err != nil {
		return engine.Resolved{}, err
	}
	steps = append(steps, links...)
	dp, d, err := pl.job(drv.Name("npm-lib", im.spec.Name), pi.system, "npm-build", n.Label, buildArgs{Steps: steps}, in)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": outPath(d)}}, nil
}

// planSite runs `vite build` (after `svelte-kit sync` for SvelteKit); the output is the static
// site directory.
func (pl *planner) planSite(n *ir.Node, im *importer) (engine.Resolved, error) {
	pi, err := pl.platformOf(n)
	if err != nil {
		return engine.Resolved{}, err
	}
	in := pl.newInputs()
	steps, _, err := pl.workspace(n, im, pi, []string{n.ID}, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	node, dirs, err := pl.buildEnv(n, im, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	vite, err := pl.binScript(n, im, "vite", "vite", "a Vite site is built with vite, it")
	if err != nil {
		return engine.Resolved{}, err
	}
	dist := work + "/dist"
	if im.spec.Kit {
		kit, err := pl.binScript(n, im, "@sveltejs/kit", "svelte-kit", "a SvelteKit site")
		if err != nil {
			return engine.Resolved{}, err
		}
		steps = append(steps, step{Op: "exec", Path: work, Argv: []string{node, kit, "sync"}, Dirs: dirs})
		dist = work + "/build" // adapter-static
	}
	argv := []string{node, vite, "build"}
	if im.spec.Config != "" {
		argv = append(argv, "--config", im.spec.Config)
	}
	out := drv.Placeholder("out")
	steps = append(steps,
		step{Op: "exec", Path: work, Argv: argv, Dirs: dirs, Env: map[string]string{"NODE_ENV": "production"}},
		step{Op: "copy", From: dist, Path: out},
		step{Op: "scrub", Path: out})
	dp, d, err := pl.job(drv.Name(siteName(n)), pi.system, "npm-build", n.Label, buildArgs{Steps: steps}, in)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": outPath(d)}}, nil
}

// siteName is the store name of a node's own output: its label name.
func siteName(n *ir.Node) string {
	_, name, _ := strings.Cut(strings.TrimPrefix(n.Label, "//"), ":")
	return name
}

// planApp bundles the program with esbuild (Node) or bun build (Bun) into lib/<name>.mjs and
// writes bin/<name>, which starts the runtime by store path. Without bundling, the runtime
// dependencies are linked into $out/node_modules and stay external.
func (pl *planner) planApp(n *ir.Node, im *importer) (engine.Resolved, error) {
	pi, err := pl.platformOf(n)
	if err != nil {
		return engine.Resolved{}, err
	}
	in := pl.newInputs()
	steps, _, err := pl.workspace(n, im, pi, []string{n.ID}, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	_, dirs, err := pl.buildEnv(n, im, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	runtime, err := pl.tool(n, im.spec.Tools.Runtime, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	name := siteName(n)
	out := drv.Placeholder("out")
	entry := "./" + path.Clean(im.spec.Entry)
	var launcher string
	switch im.spec.Runtime {
	case npmspec.RuntimeBun:
		bundle := out + "/lib/" + name + ".js"
		argv := []string{runtime + "/bin/bun", "build", entry, "--target=bun", "--outfile=" + bundle}
		if !im.spec.Bundle {
			argv = append(argv, "--packages=external")
		}
		steps = append(steps, step{Op: "exec", Path: work, Argv: argv, Dirs: dirs,
			Env: map[string]string{"BUN_INSTALL_CACHE_DIR": buildTop + "/tmp/bun", "DO_NOT_TRACK": "1"}})
		bash, err := pl.tool(n, im.spec.Tools.Bash, in)
		if err != nil {
			return engine.Resolved{}, err
		}
		launcher = "#!" + bash + "/bin/bash\nexec " + runtime + "/bin/bun " + bundle + " \"$@\"\n"
	default:
		esbuild, err := pl.tool(n, im.spec.Tools.Esbuild, in)
		if err != nil {
			return engine.Resolved{}, err
		}
		bundle := out + "/lib/" + name + ".mjs"
		argv := []string{esbuild + "/bin/esbuild", entry, "--bundle", "--platform=node", "--format=esm",
			"--log-level=warning", "--outfile=" + bundle,
			"--banner:js=import { createRequire as __bobCreateRequire } from \"node:module\"; const require = __bobCreateRequire(import.meta.url);"}
		if !im.spec.Bundle {
			argv = append(argv, "--packages=external")
		}
		steps = append(steps, step{Op: "exec", Path: work, Argv: argv, Dirs: dirs})
		q, _ := json.Marshal(bundle)
		launcher = "#!" + runtime + "/bin/node\nimport(" + string(q) + ");\n"
	}
	steps = append(steps, step{Op: "scrub", Path: out + "/lib"})
	if !im.spec.Bundle {
		links, err := pl.runtimeLinks(n, im, pi, out+"/node_modules", in)
		if err != nil {
			return engine.Resolved{}, err
		}
		steps = append(steps, links...)
	}
	steps = append(steps, step{Op: "write", Path: out + "/bin/" + name, Content: launcher, Mode: 0o755})
	dp, d, err := pl.job(drv.Name(name), pi.system, "npm-build", n.Label, buildArgs{Steps: steps}, in)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": outPath(d)}, MainProgram: "bin/" + name}, nil
}

// planTest lays out the tested importer with its test files (a workspace derivation) and runs
// vitest or node --test in a test-run job (§03.0.5).
func (pl *planner) planTest(n *ir.Node) (engine.Resolved, error) {
	var s npmspec.Test
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, internal(n, "test spec: %v", err)
	}
	if s.Of < 0 || s.Of >= len(n.Deps) {
		return engine.Resolved{}, internal(n, "test of: index out of range")
	}
	of, ok := pl.nodes[n.Deps[s.Of]]
	im, ok2 := pl.byID[n.Deps[s.Of]]
	if !ok || !ok2 {
		return engine.Resolved{}, internal(n, "the tested importer %s is not an npm importer of this repository", n.Deps[s.Of])
	}
	pi, err := pl.platformOf(n)
	if err != nil {
		return engine.Resolved{}, err
	}
	in := pl.newInputs()
	srcIDs := []string{of.ID}
	if _, ok := pl.req.Sources[n.ID]; ok {
		srcIDs = append(srcIDs, n.ID)
	}
	steps, _, err := pl.workspace(of, im, pi, srcIDs, in)
	if err != nil {
		return engine.Resolved{}, err
	}
	if im.spec.Kit {
		node, dirs, err := pl.buildEnv(of, im, in)
		if err != nil {
			return engine.Resolved{}, err
		}
		kit, err := pl.binScript(of, im, "@sveltejs/kit", "svelte-kit", "a SvelteKit site")
		if err != nil {
			return engine.Resolved{}, err
		}
		steps = append(steps, step{Op: "exec", Path: work, Argv: []string{node, kit, "sync"}, Dirs: dirs})
	}
	out := drv.Placeholder("out")
	steps = append(steps, step{Op: "copy", From: work, Path: out + "/w"})
	wsP, wsD, err := pl.job(drv.Name("npm-test-ws", siteName(n), im.dir), pi.system, "npm-build", n.Label+" workspace",
		buildArgs{Steps: steps}, in)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}

	run := pl.newInputs()
	run.addDrv(wsP)
	nodeOut, err := pl.tool(n, s.Node, run)
	if err != nil {
		return engine.Resolved{}, err
	}
	a := testRunArgs{Path: []string{work + "/node_modules/.bin", nodeOut + "/bin"}, Cwd: work,
		Copy: []copyArgs{{From: outPath(wsD) + "/w", To: work}}, TimeoutS: 600,
		Env: map[string]string{"CI": "1", "NO_COLOR": "1"}}
	switch s.Runner {
	case npmspec.RunnerNodeTest:
		a.Argv = []string{nodeOut + "/bin/node", "--test", "--test-reporter=tap"}
		a.Format = "tap"
	case npmspec.RunnerSvelteCheck:
		check, err := pl.binScript(of, im, "svelte-check", "svelte-check", "npm.SvelteCheck")
		if err != nil {
			return engine.Resolved{}, err
		}
		a.Argv = []string{nodeOut + "/bin/node", check, "--tsconfig", "./tsconfig.json", "--fail-on-warnings"}
	case npmspec.RunnerESLint:
		eslint, err := pl.binScript(of, im, "eslint", "eslint", "npm.ESLint")
		if err != nil {
			return engine.Resolved{}, err
		}
		a.Argv = []string{nodeOut + "/bin/node", eslint, "--max-warnings=0", "."}
	default:
		vitest, err := pl.binScript(of, im, "vitest", "vitest", "npm.Vitest")
		if err != nil {
			return engine.Resolved{}, err
		}
		a.Argv = []string{nodeOut + "/bin/node", vitest, "run"}
	}
	a.Argv = append(a.Argv, pl.req.TestArgs...)
	for _, sv := range s.Services {
		job := serviceJob{Name: sv.Name, Env: sv.Env, TimeoutMs: sv.TimeoutMs, Ready: probeJob{Port: sv.ReadyPort, Argv: sv.ReadyCmd}}
		var first string
		for _, ti := range sv.Tools {
			root, err := pl.tool(n, ti, run, "bin")
			if err != nil {
				return engine.Resolved{}, err
			}
			if first == "" {
				first = root
			}
			job.Path = append(job.Path, root+"/bin")
		}
		job.Argv = append([]string{}, sv.Start...)
		if len(job.Argv) > 0 && strings.Contains(job.Argv[0], "/") && !path.IsAbs(job.Argv[0]) && first != "" {
			job.Argv[0] = first + "/" + job.Argv[0]
		}
		a.Services = append(a.Services, job)
	}
	p, d, err := pl.job(drv.Name("npm-test", siteName(n), im.dir), pi.system, "test-run", n.Label, a, run)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	return engine.Resolved{Drv: p, Outputs: map[string]string{"out": outPath(d)}}, nil
}
