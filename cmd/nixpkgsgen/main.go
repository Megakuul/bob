// Command nixpkgsgen generates bob's typed nixpkgs surface (ADR 0020 §2, R-NIX-031). It is a
// maintainer tool of the root module, not a shipped executable.
//
//	go run -tags online ./cmd/nixpkgsgen snapshot [-rev <40 hex>] [-out <file>]   online, at bob release time
//	go run ./cmd/nixpkgsgen gen [-snapshot <file>]                   offline, deterministic (go generate ./pkg/nixpkgs)
//	go run ./cmd/nixpkgsgen -check                                   offline: fail on any difference
//
// snapshot evaluates the pinned nixpkgs inside bob's namespace in batches and writes
// cmd/nixpkgsgen/snapshot/<rev>.json.zst. gen turns the snapshot, exclude.json and include.json
// into pkg/nixpkgs/attrs_gen.go (split per initial letter), pkg/nixpkgs/nixext/mainprog_gen.go
// and pkg/nixpkgs/nixext/surface_gen.go.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nixpkgsgen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	top := flag.NewFlagSet("nixpkgsgen", flag.ContinueOnError)
	check := top.Bool("check", false, "regenerate in memory and fail on any difference (offline)")
	if err := top.Parse(args); err != nil {
		return err
	}
	if *check {
		return checkGenerated(root)
	}
	rest := top.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: nixpkgsgen snapshot|gen|-check")
	}
	switch rest[0] {
	case "snapshot":
		fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
		rev := fs.String("rev", "", "nixpkgs revision (default lock.DefaultRev)")
		out := fs.String("out", "", "output file (default cmd/nixpkgsgen/snapshot/<rev>.json.zst)")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if *rev == "" {
			if *rev, err = defaultRev(root); err != nil {
				return err
			}
		}
		if *out == "" {
			*out = snapshotPath(root, *rev)
		}
		inc, err := readInclude(root)
		if err != nil {
			return err
		}
		var extra []string
		for _, i := range inc {
			extra = append(extra, i.Attr)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return snapshot(ctx, root, *rev, *out, extra)
	case "gen":
		fs := flag.NewFlagSet("gen", flag.ContinueOnError)
		snap := fs.String("snapshot", "", "snapshot file (default the one of lock.DefaultRev)")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		files, err := generate(root, *snap)
		if err != nil {
			return err
		}
		return writeFiles(root, files)
	}
	return fmt.Errorf("unknown subcommand %q (snapshot, gen, -check)", rest[0])
}

// moduleRoot walks up from the working directory to the directory holding cmd/nixpkgsgen
// (go generate runs in pkg/nixpkgs).
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "cmd", "nixpkgsgen", "main.go")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cmd/nixpkgsgen not found above the working directory")
		}
		dir = parent
	}
}
