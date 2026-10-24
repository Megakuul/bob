package store

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
)

//go:generate go run ../../../cmd/bob-builder/genbuilders

// maxBuilder caps the decompressed embedded builder (a few MiB in practice).
const maxBuilder = 256 << 20

// BuilderPath returns the recorded store path of the embedded bob-builder for a Nix system
// ("aarch64-linux", "x86_64-linux"), from the generated table builders.go (R-NIX-015).
func BuilderPath(system string) (string, error) {
	if p := builderTable(system); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("store: no bob-builder for system %q", system)
}

// HostSystem is the Nix system of this bob binary.
func HostSystem() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64-linux"
	case "amd64":
		return "x86_64-linux"
	}
	return runtime.GOARCH + "-linux"
}

// Builder makes the embedded bob-builder of the host system valid in the store and returns its
// store path: the `source` path of the tree bin/bob-builder named bob-builder-<ver>. A binary
// whose path differs from builders.go is refused with BOB-E-9007.
func (s *Store) Builder(ctx context.Context) (string, error) {
	want, err := BuilderPath(HostSystem())
	if err != nil {
		return "", err
	}
	// A valid path holds exactly the content its name was computed from, so the embedded
	// binary (a 40 ms gunzip) is read and checked against the table only when it is imported.
	invalid, err := s.Invalid(ctx, []string{want})
	if err != nil || len(invalid) == 0 {
		return want, err
	}
	item, err := embeddedBuilder(want)
	if err != nil {
		return "", err
	}
	if err := s.Import(ctx, []ExportItem{item}); err != nil {
		return "", fmt.Errorf("store: import bob-builder: %w", err)
	}
	return want, nil
}

// embeddedBuilder reads the embedded binary and checks that its store path is want.
func embeddedBuilder(want string) (ExportItem, error) {
	missing := diag.New(7003, "what", "this bob was built without its embedded builder (run go generate ./pkg/engine/store before go build)")
	f, err := builderFS.Open(builderDir + "/bob-builder.gz")
	if err != nil {
		return ExportItem{}, missing
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return ExportItem{}, missing
	}
	bin, err := io.ReadAll(io.LimitReader(zr, maxBuilder+1))
	if err != nil || len(bin) > maxBuilder {
		return ExportItem{}, missing
	}
	name := want[strings.LastIndex(want, "/")+1:]
	if len(name) < 34 {
		return ExportItem{}, fmt.Errorf("store: recorded builder path %q is malformed", want)
	}
	nar := builderNAR(bin)
	got, err := drv.MakeStorePath("source", sha256.Sum256(nar), name[33:])
	if err != nil {
		return ExportItem{}, err
	}
	if got != want {
		return ExportItem{}, diag.New(9007)
	}
	return ExportItem{Path: want, NAR: nar}, nil
}

// builderNAR is the NAR of the tree bin/bob-builder (an executable).
func builderNAR(bin []byte) []byte {
	var out bytes.Buffer
	e := &enc{w: &out}
	for _, t := range []string{"nix-archive-1", "(", "type", "directory", "entry", "(", "name", "bin", "node",
		"(", "type", "directory", "entry", "(", "name", "bob-builder", "node",
		"(", "type", "regular", "executable", "", "contents"} {
		e.str(t)
	}
	e.bytes(bin)
	for range 5 {
		e.str(")")
	}
	return out.Bytes()
}
