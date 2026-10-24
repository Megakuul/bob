package gen

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/srcs"
)

// runtimeFS holds the prebuilt bob AppImage runtime (runtime/, a separate Go module) for both
// architectures, written by `go test ./pkg/output/appimage/gen -run TestRuntimeEmbedded
// -update-runtime` (TestRuntimeEmbedded fails when they are stale). SPEC O-04-2: bob embeds
// the runtimes and adds them to the store by content; no bundle compiles anything.
//
//go:embed runtimebin/arm64/bobrun.gz runtimebin/amd64/bobrun.gz
var runtimeFS embed.FS

// maxRuntime bounds the decompressed runtime (R-OUT-025: ≤ 3 MiB).
const maxRuntime = 3 << 20

// goarch is the Go architecture of an AppImage architecture.
var goarch = map[string]string{"x86_64": "amd64", "aarch64": "arm64"}

// runtimeBinary returns the embedded runtime for an AppImage architecture.
func runtimeBinary(arch string) ([]byte, error) {
	f, err := runtimeFS.Open("runtimebin/" + goarch[arch] + "/bobrun.gz")
	if err != nil {
		return nil, fmt.Errorf("this bob was built without its AppImage runtime for %s: %w", arch, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("embedded AppImage runtime for %s: %w", arch, err)
	}
	b, err := io.ReadAll(io.LimitReader(zr, maxRuntime+1))
	if err != nil || len(b) > maxRuntime {
		return nil, fmt.Errorf("embedded AppImage runtime for %s is unreadable or too large", arch)
	}
	return b, nil
}

// importRuntime adds the runtime for arch to the store as the source path
// bob-appimage-runtime-<arch> (tree bin/bobrun) and returns its store path.
func importRuntime(ctx context.Context, st engine.PlanStore, arch string) (string, error) {
	bin, err := runtimeBinary(arch)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "bob-appimage-runtime-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "bin"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "bin", "bobrun"), bin, 0o755); err != nil {
		return "", err
	}
	paths, err := st.ImportSources(ctx, []srcs.Source{{Name: "bob-appimage-runtime-" + arch,
		Tree: srcs.Tree{Root: tmp, Files: []string{"bin/bobrun"}}}})
	if err != nil {
		return "", fmt.Errorf("import the AppImage runtime: %w", err)
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("import the AppImage runtime: no path")
	}
	return paths[0], nil
}

// gzipBytes is a reproducible gzip (no name, no time).
func gzipBytes(b []byte) ([]byte, error) {
	var out bytes.Buffer
	zw, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
