package gen

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var updateRuntime = flag.Bool("update-runtime", false, "rebuild runtime/ for both architectures and rewrite runtimebin/")

// TestRuntimeEmbedded rebuilds the AppImage runtime (static, CGO_ENABLED=0, trimmed, no build
// id) for both architectures and compares it with the embedded binaries (R-OUT-015, O-04-2).
func TestRuntimeEmbedded(t *testing.T) {
	if testing.Short() && !*updateRuntime {
		t.Skip("builds the runtime twice")
	}
	for _, arch := range []string{"aarch64", "x86_64"} {
		out := filepath.Join(t.TempDir(), "bobrun")
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-ldflags=-s -w -buildid=", "-o", out, ".")
		cmd.Dir = "runtime"
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch[arch], "GOWORK=off", "GOFLAGS=")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build runtime (%s): %v\n%s", arch, err, b)
		}
		bin, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if len(bin) > maxRuntime {
			t.Errorf("runtime for %s is %d bytes, budget %d (R-OUT-025)", arch, len(bin), maxRuntime)
		}
		if *updateRuntime {
			gz, err := gzipBytes(bin)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("runtimebin", goarch[arch], "bobrun.gz"), gz, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := runtimeBinary(arch)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(have, bin) {
			t.Errorf("the embedded AppImage runtime for %s is stale: run go test ./pkg/output/appimage/gen -run TestRuntimeEmbedded -update-runtime", arch)
		}
	}
}
