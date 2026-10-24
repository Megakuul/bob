// Command bob-npm is the S4 spike driver: lock (own resolver), compare (vs package-lock.json),
// and (later) build/drvs for node_modules and builders.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bobspike/s04/npm"
)

func main() {
	if err := runCmd(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bob-npm:", err)
		os.Exit(1)
	}
}

func cacheDir() string {
	if d := os.Getenv("BOB_NPM_CACHE"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("XDG_CACHE_HOME"), "bob-s04", "registry")
}

func runCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: bob-npm lock|compare|build <dir>")
	}
	dir := args[1]
	switch args[0] {
	case "lock":
		in, err := npm.InputFromDir(dir)
		if err != nil {
			return err
		}
		reg := npm.NewRegistry(cacheDir())
		reg.Offline = os.Getenv("BOB_NPM_OFFLINE") != ""
		t0 := time.Now()
		lock, err := npm.Resolve(context.Background(), reg, in)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(lock, "", " ")
		if err := os.WriteFile(filepath.Join(dir, "bob.lock.json"), b, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "resolved %d package instances in %.2fs (registry: %d fetched, %d revalidated, %.1f MB)\n",
			len(lock.Packages), time.Since(t0).Seconds(), reg.Stats.Fetched, reg.Stats.Cached, float64(reg.Stats.Bytes)/1e6)
		for _, w := range lock.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	case "compare":
		var nl npm.NpmLock
		var bl npm.Lock
		if err := readJSON(args[2], &nl); err != nil {
			return err
		}
		if err := readJSON(filepath.Join(dir, "bob.lock.json"), &bl); err != nil {
			return err
		}
		r := npm.Compare(&nl, &bl)
		fmt.Printf("npm=%d bob=%d common=%d onlyNpm=%d onlyBob=%d bundledSkipped=%d edges=%d edgeDiffs=%d\n",
			r.NpmPkgs, r.BobPkgs, r.Common, len(r.OnlyNpm), len(r.OnlyBob), r.BundledSkipped, r.EdgesChecked, r.EdgeDiffs)
		for _, x := range r.OnlyNpm {
			fmt.Println("  only npm:", x)
		}
		for _, x := range r.OnlyBob {
			fmt.Println("  only bob:", x)
		}
		for _, x := range r.EdgeDiffList {
			fmt.Println("  edge:", x)
		}
	case "materialise":
		return materialise(dir, args[2])
	case "paths":
		return paths(dir, args[2], args[3])
	case "build":
		return build(dir, args[2:])
	default:
		return fmt.Errorf("unknown command %s", args[0])
	}
	return nil
}

func readJSON(f string, v any) error {
	b, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
