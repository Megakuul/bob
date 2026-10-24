package gen

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	npmgen "github.com/megakuul/bob/pkg/npm/gen"
)

var updateToolLock = flag.Bool("update-tool-lock", false, "re-resolve the graphql-codegen tool lock from the public registry")

// TestToolLock checks the embedded tool lock: one importer "tool" with exactly the declared
// dependencies at their versions, every package with a sha512 integrity and no install script.
// With -update-tool-lock it is re-resolved first (online, at bob release time).
func TestToolLock(t *testing.T) {
	if *updateToolLock {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		tl, err := npmgen.ResolveToolLock(ctx, toolDeps, "24.21.0")
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(tl, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("toollock/graphql-codegen.json", append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	tl, err := toolLock()
	if err != nil {
		t.Fatal(err)
	}
	im, ok := tl.Importers[npmgen.ToolImporter]
	if !ok || len(tl.Importers) != 1 {
		t.Fatalf("importers %v, want one %q", tl.Importers, npmgen.ToolImporter)
	}
	if len(im.Deps) != len(toolDeps) {
		t.Errorf("tool deps %v, want %v", im.Deps, toolDeps)
	}
	for name, version := range toolDeps {
		p, ok := tl.Packages[im.Deps[name]]
		if !ok || p.Name != name || p.Version != version {
			t.Errorf("%s is locked as %+v, want version %s", name, p, version)
		}
	}
	for k, p := range tl.Packages {
		if len(p.Integrity) < 7 || p.Integrity[:7] != "sha512-" || p.HasInstallScript {
			t.Errorf("%s: integrity %q, install script %v", k, p.Integrity, p.HasInstallScript)
		}
	}
}
