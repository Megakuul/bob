// Command bob-rs is spike S3's driver (stand-in for `bob lock` / `bob build` / `bob dev` on Rust).
//
//	bob-rs lock    -ws WS.json -o bob.lock.json [-cargo-lock Cargo.lock] [-offline]
//	bob-rs cargo   -ws WS.json -lock bob.lock.json -out DIR      generated Cargo.toml/Cargo.lock/rust-project.json
//	bob-rs build   -ws WS.json -lock bob.lock.json -target CRATE [-v] [-timing]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bobspike/s03/crates"
)

func cacheDir() string {
	d := os.Getenv("XDG_CACHE_HOME")
	if d == "" {
		d = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(d, "bob-s03")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bob-rs lock|cargo|build ...")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	ws := fs.String("ws", "", "workspace JSON")
	out := fs.String("o", "", "output")
	lockf := fs.String("lock", "", "bob.lock JSON")
	cargoLock := fs.String("cargo-lock", "", "also write Cargo.lock here")
	offline := fs.Bool("offline", false, "index cache only")
	noMeta := fs.Bool("no-meta", false, "skip crate downloads (resolution only)")
	target := fs.String("target", "", "crate to build")
	verbose := fs.Bool("v", false, "raw Nix output")
	timing := fs.Bool("timing", false, "phase timings")
	outDir := fs.String("out", "", "directory")
	_ = fs.Parse(os.Args[2:])
	var err error
	switch os.Args[1] {
	case "lock":
		err = cmdLock(*ws, *out, *cargoLock, *offline, *noMeta)
	case "ide":
		err = cmdIDE(*ws, *lockf, *target, *out)
	case "cargo":
		err = cmdCargo(*ws, *lockf, *outDir)
		if err == nil && *lockf != "" {
			err = cmdVendor(*ws, *lockf)
		}
	case "build", "drvs", "outs":
		err = cmdBuild(os.Args[1], *ws, *lockf, *target, *verbose, *timing)
	default:
		err = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cmdLock(wsPath, out, cargoLock string, offline, noMeta bool) error {
	w, err := crates.ReadWorkspace(wsPath)
	if err != nil {
		return err
	}
	ix := crates.NewIndex(cacheDir())
	ix.Offline = offline
	t0 := time.Now()
	res, err := crates.Resolve(context.Background(), ix, w.Locals(), crates.ResolveOpts{RustVersion: "1.95.0"})
	if err != nil {
		return fmt.Errorf("✗ cannot resolve crates: %v", err)
	}
	t1 := time.Now()
	l := res.ToLock()
	if !noMeta {
		if err := crates.FillMeta(context.Background(), cacheDir(), l, 8); err != nil {
			return err
		}
	}
	t2 := time.Now()
	fmt.Fprintf(os.Stderr, "resolved %d crates.io crates in %v (%d passes, %d index files, %d HTTP requests); metadata %v\n",
		len(l.Crates), t1.Sub(t0).Round(time.Millisecond), res.Stats.Iterations, res.Stats.IndexFiles, ix.Fetched, t2.Sub(t1).Round(time.Millisecond))
	if cargoLock != "" {
		if err := os.WriteFile(cargoLock, []byte(res.CargoLock()), 0o644); err != nil {
			return err
		}
	}
	if out != "" {
		return l.Write(out)
	}
	return nil
}
