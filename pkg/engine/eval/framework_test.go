package eval

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateFramework = flag.Bool("update-framework", false, "rewrite framework.zip from the source tree")

// TestFrameworkZip checks that the embedded framework module equals the source tree's L0–L3
// packages (R-FW-043.4). With -update-framework (go generate ./pkg/engine/eval) it rewrites it.
func TestFrameworkZip(t *testing.T) {
	files, err := frameworkSource()
	if err != nil {
		t.Skipf("no framework source tree: %v", err)
	}
	want, err := zipFramework(files)
	if err != nil {
		t.Fatal(err)
	}
	if *updateFramework {
		if err := os.WriteFile(filepath.Join(".", "framework.zip"), want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(frameworkZip, want) {
		t.Errorf("framework.zip is stale (the L0–L3 sources changed): run go generate ./pkg/engine/eval")
	}
	got, err := unzipFramework(frameworkZip)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(files) {
		t.Errorf("framework.zip has %d files, the source tree %d", len(got), len(files))
	}
	if _, ok := files["pkg/bob/core.go"]; !ok {
		t.Errorf("framework files lack pkg/bob/core.go: %d files", len(files))
	}
	for n := range files {
		if filepath.Base(filepath.Dir(n)) == "gen" || filepath.Base(filepath.Dir(n)) == "bop" {
			t.Errorf("framework module contains engine code %s", n)
		}
	}
}

func TestFrameworkHash(t *testing.T) {
	fw, err := newFramework("v1.2.3", map[string][]byte{
		"go.mod":         []byte("module github.com/megakuul/bob\n\ngo 1.26.9\n"),
		"pkg/bob/bob.go": []byte("package bob\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The go command accepted this go.sum line for this go.mod (checked by hand with -mod=readonly).
	if fw.modH1 != "h1:FVPIixO13ek9VjWK2KpMEpNXSwUkHF9ytq/s/8niFGc=" {
		t.Errorf("go.mod hash %s", fw.modH1)
	}
	if fw.goDir != "1.26.9" {
		t.Errorf("go directive %q", fw.goDir)
	}
	if got := fw.packages(); len(got) != 1 || got[0] != frameworkModule+"/pkg/bob" {
		t.Errorf("packages %v", got)
	}
}
