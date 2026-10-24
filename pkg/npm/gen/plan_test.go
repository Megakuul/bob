package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/npm/bop"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
)

const (
	nodeOut = "/nix/store/00000000000000000000000000000001-nodejs-24.21.0"
	bashOut = "/nix/store/00000000000000000000000000000002-bash-5.3"
	slimOut = "/nix/store/00000000000000000000000000000003-nodejs-slim-24.21.0"
	esbOut  = "/nix/store/00000000000000000000000000000004-esbuild-0.27.2"
	builder = "/nix/store/00000000000000000000000000000005-bob-builder-6"
	libSrc  = "/nix/store/00000000000000000000000000000006-lib-src"
	appSrc  = "/nix/store/00000000000000000000000000000007-app-src"
	testSrc = "/nix/store/00000000000000000000000000000008-test-src"
)

const integrity = "sha512-z4PhNX7vuL3xVChQ1m2AB9Yg5AULVxXcg/SpIdNs6c5H0NE8XYXysP+DGNKHfuwvY7kxvUdBeoGlODJ6+SfaPg=="

func pkg(name, version string, extra string) string {
	return fmt.Sprintf(`"%s@%s": {"name": %q, "version": %q, "integrity": %q%s}`, name, version, name, version, integrity, extra)
}

// testLock: lib (zod, typescript dev, a cycle a↔b through zod), app (link to lib, esbuild with
// a platform package per OS, better-sqlite3 with an install script).
var testLock = `{"inputs": "` + strings.Repeat("0", 64) + `", "registry": "https://registry.npmjs.org/",
 "importers": {
  "lib": {"deps": {"zod": "zod@4.0.0", "typescript": "typescript@5.9.3"}, "dev": ["typescript"]},
  "app": {"deps": {"@t/lib": "link:lib", "esbuild": "esbuild@0.25.0", "better-sqlite3": "better-sqlite3@12.0.0", "vitest": "vitest@3.0.0"}, "dev": ["vitest"]}
 },
 "packages": {
  ` + pkg("zod", "4.0.0", `, "deps": {"a": "a@1.0.0"}`) + `,
  ` + pkg("a", "1.0.0", `, "deps": {"b": "b@1.0.0"}`) + `,
  ` + pkg("b", "1.0.0", `, "deps": {"a": "a@1.0.0"}`) + `,
  ` + pkg("typescript", "5.9.3", `, "bin": {"tsc": "bin/tsc"}`) + `,
  ` + pkg("vitest", "3.0.0", `, "bin": {"vitest": "vitest.mjs"}`) + `,
  ` + pkg("esbuild", "0.25.0", `, "hasInstallScript": true, "deps": {"@esbuild/linux-arm64": "@esbuild/linux-arm64@0.25.0", "@esbuild/darwin-arm64": "@esbuild/darwin-arm64@0.25.0"}, "bin": {"esbuild": "bin/esbuild"}`) + `,
  ` + pkg("@esbuild/linux-arm64", "0.25.0", `, "optional": true, "os": ["linux"], "cpu": ["arm64"]`) + `,
  ` + pkg("@esbuild/darwin-arm64", "0.25.0", `, "optional": true, "os": ["darwin"], "cpu": ["arm64"]`) + `,
  ` + pkg("better-sqlite3", "12.0.0", `, "hasInstallScript": true, "deps": {"bindings": "bindings@1.5.0"}`) + `,
  ` + pkg("bindings", "1.5.0", ``) + `
 }}`

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testGraph is a library and an app linking it, plus a vitest test of the app, for one
// platform. hoisted switches the app to npm.Hoisted().
func testGraph(t *testing.T, hoisted bool) (*ir.Graph, map[string]engine.Resolved, map[string]string) {
	t.Helper()
	proj := map[string]json.RawMessage{"platform": json.RawMessage(`"linux-arm64"`)}
	tools := func() npmspec.Tools {
		return npmspec.Tools{Node: 0, Bash: 1, Runtime: -1, Esbuild: -1, Scripts: []int{}}
	}
	ts := npmspec.TSConfig{Target: "ES2022", Module: "NodeNext", ModuleResolution: "NodeNext", Strict: true,
		Lib: []string{}, Types: []string{}, Include: []string{"src"}, Exclude: []string{}, Extra: map[string]any{}}
	libSpec := npmspec.Importer{Name: "@t/lib", Entry: "src/index.ts", TSConfig: ts, Tools: tools(),
		Deps:  []npmspec.Dep{{Alias: "zod", Req: "^4", Link: -1}, {Alias: "typescript", Req: "~5.9.2", Dev: true, Link: -1}},
		Peers: []npmspec.Dep{}, Options: npmspec.Options{AllowScripts: []string{}, Extend: []npmspec.Extend{}}}
	appTools := tools()
	appTools.Runtime, appTools.Esbuild, appTools.Scripts = 3, 4, []int{}
	appSpec := npmspec.Importer{Name: "app", Entry: "src/main.ts", TSConfig: ts, Tools: appTools, Runtime: npmspec.RuntimeNode, Bundle: false,
		Deps: []npmspec.Dep{{Link: 2}, {Alias: "esbuild", Req: "^0.25", Link: -1}, {Alias: "better-sqlite3", Req: "^12", Link: -1},
			{Alias: "vitest", Req: "^3", Dev: true, Link: -1}},
		Peers: []npmspec.Dep{}, Options: npmspec.Options{AllowScripts: []string{"better-sqlite3", "esbuild"}, Extend: []npmspec.Extend{}, Hoisted: hoisted}}
	g := &ir.Graph{Nodes: []*ir.Node{
		{ID: "lib", Kind: npmspec.KindLib, Label: "//lib:lib", Proj: proj, Deps: []string{"nixpkgs:nodejs", "nixpkgs:bash"}, Spec: mustJSON(t, libSpec)},
		{ID: "app", Kind: npmspec.KindApp, Label: "//app:app", Proj: proj, Deps: []string{"nixpkgs:nodejs", "nixpkgs:bash", "lib", "nixpkgs:nodejs-slim", "nixpkgs:esbuild"}, Spec: mustJSON(t, appSpec)},
		{ID: "test", Kind: npmspec.KindTest, Label: "//app:test", Proj: proj, Deps: []string{"app", "nixpkgs:nodejs"},
			Spec: mustJSON(t, npmspec.Test{Of: 0, Runner: npmspec.RunnerVitest, Node: 1, Services: nil})},
	}}
	inputs := map[string]engine.Resolved{
		"nixpkgs:nodejs":      {Outputs: map[string]string{"out": nodeOut}},
		"nixpkgs:bash":        {Outputs: map[string]string{"out": bashOut}},
		"nixpkgs:nodejs-slim": {Outputs: map[string]string{"out": slimOut}},
		"nixpkgs:esbuild":     {Outputs: map[string]string{"out": esbOut}},
	}
	return g, inputs, map[string]string{"lib": libSrc, "app": appSrc, "test": testSrc}
}

func planTestGraph(t *testing.T, hoisted bool) engine.PlanResult {
	t.Helper()
	g, inputs, srcs := testGraph(t, hoisted)
	res, err := Ecosystem{}.Plan(context.Background(), engine.PlanRequest{Graph: g, Nodes: g.Nodes, Lock: json.RawMessage(testLock),
		Inputs: inputs, Sources: srcs, Builder: builder})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return res
}

// jobOf returns the bobSpec of a derivation.
func jobOf(t *testing.T, d *drv.Derivation) bspec.Job {
	t.Helper()
	raw, err := json.Marshal(d.Structured["bobSpec"])
	if err != nil {
		t.Fatal(err)
	}
	var j bspec.Job
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		t.Fatalf("bobSpec: %v", err)
	}
	return j
}

// TestPlanJobsDecodeStrictly: every planned job decodes into bop's (or the core test-run's)
// argument types with unknown fields rejected, as bob-builder reads them.
func TestPlanJobsDecodeStrictly(t *testing.T) {
	for _, hoisted := range []bool{false, true} {
		res := planTestGraph(t, hoisted)
		kinds := map[string]int{}
		for _, d := range res.Drvs {
			j := jobOf(t, d)
			kinds[j.Kind]++
			var v any
			switch j.Kind {
			case "npm-unpack":
				v = &bop.UnpackArgs{}
			case "npm-node-modules":
				v = &bop.NodeModulesArgs{}
			case "npm-build":
				v = &bop.BuildArgs{}
			case "test-run":
				v = &bspec.TestRunArgs{}
			default:
				t.Fatalf("unknown job kind %s", j.Kind)
			}
			dec := json.NewDecoder(bytes.NewReader(j.Args))
			dec.DisallowUnknownFields()
			if err := dec.Decode(v); err != nil {
				t.Errorf("%s (%s): %v", d.Name, j.Kind, err)
			}
		}
		if kinds["test-run"] != 1 || kinds["npm-build"] != 3 || kinds["npm-node-modules"] != 2 {
			t.Errorf("hoisted=%v: job kinds %v, want 1 test-run, 3 npm-build (lib, app, test workspace), 2 node_modules", hoisted, kinds)
		}
	}
}

// TestPlanLayout checks the isolated layout: one derivation per instance, the a↔b cycle in one
// group, the darwin-only optional package left out on linux-arm64, scripts only where allowed,
// tarballs fetched once each, and a deterministic result.
func TestPlanLayout(t *testing.T) {
	res := planTestGraph(t, false)
	names := map[string]bool{}
	for _, d := range res.Drvs {
		names[d.Name] = true
	}
	for _, want := range []string{"npm-zod-4.0.0", "npm-esbuild-0.25.0", "npm--esbuild-linux-arm64-0.25.0",
		"npm-better-sqlite3-12.0.0-script", "npm-bindings-1.5.0", "npm-lib--t-lib", "app", "node_modules-app", "node_modules-lib"} {
		if !names[want] {
			t.Errorf("no derivation %s in %v", want, names)
		}
	}
	scc := 0
	for n := range names {
		if strings.HasPrefix(n, "npm-scc-") {
			scc++
		}
		if strings.Contains(n, "darwin") || n == "npm-esbuild-0.25.0-script" || n == "npm-a-1.0.0" {
			t.Errorf("unexpected derivation %s", n)
		}
	}
	if scc != 1 {
		t.Errorf("cycle groups: %d, want 1 (a↔b)", scc)
	}
	seen := map[string]bool{}
	for _, f := range res.Fetches {
		if seen[f.Name] {
			t.Errorf("tarball %s fetched twice", f.Name)
		}
		seen[f.Name] = true
		if strings.Contains(f.Name, "darwin") {
			t.Errorf("darwin package fetched for linux-arm64: %s", f.Name)
		}
	}
	again := planTestGraph(t, false)
	if len(again.Drvs) != len(res.Drvs) {
		t.Fatalf("second plan has %d derivations, first %d", len(again.Drvs), len(res.Drvs))
	}
	for i := range res.Drvs {
		a, _ := res.Drvs[i].Path()
		b, _ := again.Drvs[i].Path()
		if a != b {
			t.Errorf("plan is not deterministic: %s vs %s", a, b)
		}
	}
	if res.Nodes["app"].MainProgram != "bin/app" {
		t.Errorf("app main program %q", res.Nodes["app"].MainProgram)
	}
}

// TestPlanHoisted: npm.Hoisted() gives one node_modules-<importer>-hoisted derivation that
// takes the tarballs itself (no instance derivations for that importer).
func TestPlanHoisted(t *testing.T) {
	res := planTestGraph(t, true)
	hoisted := 0
	for _, d := range res.Drvs {
		if d.Name == "node_modules-app-hoisted" {
			hoisted++
			var a bop.NodeModulesArgs
			if err := json.Unmarshal(jobOf(t, d).Args, &a); err != nil {
				t.Fatal(err)
			}
			paths := map[string]bool{}
			for _, e := range a.Hoisted {
				paths[e.Path] = true
				if e.Path == "better-sqlite3" && e.Script == nil {
					t.Errorf("the allowed install script of better-sqlite3 is not run")
				}
				if e.Path == "esbuild" && e.Script != nil {
					t.Errorf("esbuild's install script runs although the quirks table skips it")
				}
			}
			for _, p := range []string{"@t/lib", "esbuild", "@esbuild/linux-arm64", "better-sqlite3", "bindings", "vitest"} {
				if !paths[p] {
					t.Errorf("hoisted tree lacks %s: %v", p, paths)
				}
			}
		}
	}
	if hoisted != 1 {
		t.Errorf("hoisted derivations: %d, want 1", hoisted)
	}
}

func TestNativeFiles(t *testing.T) {
	g, _, _ := testGraph(t, false)
	files, err := Ecosystem{}.Native(context.Background(), engine.PlanRequest{Graph: g, Lock: json.RawMessage(testLock)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = string(f.Content)
	}
	want := `{
  "//": "Code generated by bob from bob.go. DO NOT EDIT.",
  "name": "@t/lib",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "exports": {
    ".": {
      "types": "./dist/index.d.ts",
      "import": "./dist/index.js"
    }
  },
  "types": "./dist/index.d.ts",
  "dependencies": {
    "zod": "4.0.0"
  },
  "devDependencies": {
    "typescript": "5.9.3"
  }
}
`
	if got["lib/package.json"] != want {
		t.Errorf("lib/package.json:\n%s\nwant:\n%s", got["lib/package.json"], want)
	}
	if !strings.Contains(got["app/package.json"], `"@t/lib": "file:../lib"`) {
		t.Errorf("app/package.json does not link the library:\n%s", got["app/package.json"])
	}
	tc := got["lib/tsconfig.json"]
	if !strings.HasPrefix(tc, "// Code generated by bob") || !strings.Contains(tc, `"strict": true`) || !strings.Contains(tc, `"outDir": "dist"`) {
		t.Errorf("lib/tsconfig.json:\n%s", tc)
	}
	for _, f := range files {
		if f.Path == "lib/tsconfig.json" {
			lines := f.Explain([]byte(strings.Replace(tc, `"strict": true`, `"strict": false`, 1)))
			if len(lines) != 1 || lines[0].Op != "~" || !strings.Contains(lines[0].Text, "strict") {
				t.Errorf("drift lines %+v", lines)
			}
		}
	}
}

func TestStale(t *testing.T) {
	g, _, _ := testGraph(t, false)
	ctx := context.Background()
	if what, _, err := (Ecosystem{}).Stale(ctx, engine.LockRequest{Graph: g, Prev: json.RawMessage(testLock)}); err != nil || what != "" {
		t.Fatalf("fresh lock reported stale: %q %v", what, err)
	}
	var spec npmspec.Importer
	_ = json.Unmarshal(g.Nodes[0].Spec, &spec)
	spec.Deps[0].Req = "^3"
	g.Nodes[0].Spec = mustJSON(t, spec)
	what, at, err := (Ecosystem{}).Stale(ctx, engine.LockRequest{Graph: g, Prev: json.RawMessage(testLock)})
	if err != nil || what != "npm zod ^3 is locked at 4.0.0" || at != g.Nodes[0].DefinedAt {
		t.Errorf("stale = %q at %v, %v", what, at, err)
	}
	spec.Deps[0].Req = "^4"
	spec.Deps = append(spec.Deps, npmspec.Dep{Alias: "left-pad", Req: "^1", Link: -1})
	g.Nodes[0].Spec = mustJSON(t, spec)
	if what, _, _ := (Ecosystem{}).Stale(ctx, engine.LockRequest{Graph: g, Prev: json.RawMessage(testLock)}); what != "npm left-pad ^1 is not locked" {
		t.Errorf("new dependency: %q", what)
	}
}
