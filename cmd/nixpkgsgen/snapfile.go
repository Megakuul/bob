package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/klauspost/compress/zstd"
)

// Snapshot is cmd/nixpkgsgen/snapshot/<rev>.json.zst: the facts gen needs about every
// top-level attribute of one nixpkgs revision (ADR 0020 §2), taken on System.
type Snapshot struct {
	Rev    string          `json:"rev"`
	System string          `json:"system"`
	Names  []string        `json:"names"` // builtins.attrNames of the package set (the pinned index's names list)
	Attrs  map[string]Info `json:"attrs"`
}

// Info is one attribute of the snapshot.
type Info struct {
	Drv         bool     `json:"drv"`                   // a derivation (sets, scopes and functions are not)
	Failed      bool     `json:"failed,omitempty"`      // the attribute itself could not be evaluated
	Alias       bool     `json:"alias,omitempty"`       // gone with config.allowAliases = false
	Version     string   `json:"version,omitempty"`     // version
	Description string   `json:"description,omitempty"` // meta.description
	Licenses    []string `json:"licenses,omitempty"`    // SPDX ids of meta.license
	MainProgram string   `json:"mainProgram,omitempty"` // meta.mainProgram
	Outputs     []string `json:"outputs,omitempty"`
	Platforms   []string `json:"platforms,omitempty"` // of aarch64-linux, x86_64-linux
	Broken      bool     `json:"broken,omitempty"`
	Insecure    bool     `json:"insecure,omitempty"` // meta.knownVulnerabilities non-empty
	Unfree      bool     `json:"unfree,omitempty"`
	EvalOK      bool     `json:"evalOk,omitempty"` // drvPath evaluates (allowUnfree = true)
}

const maxSnapshot = 256 << 20 // decompressed snapshot cap

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// snapshotPath is the committed snapshot of rev.
func snapshotPath(root, rev string) string {
	return filepath.Join(root, "cmd", "nixpkgsgen", "snapshot", rev+".json.zst")
}

// readSnapshot reads and validates a snapshot file.
func readSnapshot(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	b, err := io.ReadAll(io.LimitReader(dec, maxSnapshot+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(b) > maxSnapshot {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, maxSnapshot)
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !hex40.MatchString(s.Rev) || len(s.Names) == 0 || s.Attrs == nil {
		return nil, fmt.Errorf("%s: not a nixpkgsgen snapshot", path)
	}
	return &s, nil
}
