// bob-ml: spike driver for OCaml/opam in bob (S5).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bobspike/s05/opam"
)

const nixpkgsRev = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"

// Toolchain versions provided by nixpkgs (ocamlPackages of the pin), fixed in resolution.
var Provided = map[string]string{
	"ocaml": "5.4.1", "ocaml-base-compiler": "5.4.1", "ocaml-compiler": "5.4.1", "ocaml-config": "3",
	"ocaml-options-vanilla": "1", "base-unix": "base", "base-threads": "base", "base-bigarray": "base",
	"base-domains": "base", "base-nnp": "base", "base-effects": "base",
	"dune": "3.21.1", "ocamlfind": "1.9.8",
}

func repoDir() string {
	if d := os.Getenv("OPAM_REPO"); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(os.Args[0]), "..", "tmp", "repo")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bob-ml parseall|resolve|lock|build|ide ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "parseall":
		err = parseAll()
	case "resolve":
		err = resolveCmd(os.Args[2:])
	case "lock":
		err = lockCmd(os.Args[2:])
	case "build", "drvs", "outs":
		err = buildCmd(os.Args[1], os.Args[2:])
	case "latest":
		err = latestCmd(os.Args[2:])
	case "ide":
		err = ideCmd(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseAll() error {
	t0 := time.Now()
	n, bad := 0, 0
	root := filepath.Join(repoDir(), "packages")
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || fi.Name() != "opam" {
			return nil
		}
		n++
		b, _ := os.ReadFile(p)
		f, err := opam.Parse(string(b))
		if err == nil {
			// also exercise the dependency normaliser
			_, err = opam.Deps(f.Fields["depends"], func(string) (string, bool) { return "", false })
		}
		if err != nil {
			bad++
			if bad <= 5 {
				fmt.Fprintf(os.Stderr, "%s: %v\n", strings.TrimPrefix(p, root+"/"), err)
			}
		}
		return nil
	})
	fmt.Printf("parsed %d opam files, %d failed, %.2fs\n", n, bad, time.Since(t0).Seconds())
	return nil
}

func newResolver() *opam.Resolver {
	return &opam.Resolver{Repo: opam.OpenRepo(repoDir()), Plat: opam.DefaultPlatform(Provided["ocaml"]), Provided: Provided}
}

func resolveCmd(args []string) error {
	t0 := time.Now()
	r := newResolver()
	var roots []opam.Atom
	for _, a := range args {
		roots = append(roots, opam.Atom{Name: a})
	}
	s, err := r.Resolve(roots)
	if err != nil {
		return fmt.Errorf("✗ cannot resolve opam packages: %v", err)
	}
	var names []string
	for n, p := range s.Pkgs {
		if p.F != nil {
			names = append(names, n+"."+p.Version)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
	fmt.Fprintf(os.Stderr, "%d packages, %d opam files parsed, %d steps, %.3fs\n", len(names), r.Repo.Parsed, r.Steps, time.Since(t0).Seconds())
	return nil
}

// latestCmd prints "name version" of the newest version of every package (or of args).
func latestCmd(args []string) error {
	r := newResolver()
	if len(args) == 0 {
		ents, _ := os.ReadDir(filepath.Join(repoDir(), "packages"))
		for _, e := range ents {
			args = append(args, e.Name())
		}
	}
	for _, n := range args {
		vs, err := r.Repo.Versions(n)
		if err == nil && len(vs) > 0 {
			fmt.Println(n, vs[0].Version)
		}
	}
	return nil
}
