package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bobspike/s05/ocamlgen"
	"bobspike/s05/opam"
)

// Workspace is the stand-in for bob.go.
type Workspace struct {
	Root string   `json:"-"`
	Opam []string `json:"opam"` // opam package roots
	C    []*CLib  `json:"c"`
	Libs []*MLLib `json:"libs"`
	Exes []*MLExe `json:"exes"`
}
type CLib struct{ Name, Src string }
type MLLib = ocamlgen.MLLib
type MLExe = ocamlgen.MLExe

func readWS(path string) (*Workspace, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	w := &Workspace{}
	if err := json.Unmarshal(b, w); err != nil {
		return nil, err
	}
	w.Root, _ = filepath.Abs(filepath.Dir(path))
	return w, nil
}

func repoCommit() string {
	b, _ := os.ReadFile(filepath.Join(repoDir(), "..", "repo.commit"))
	return strings.TrimSpace(string(b))
}

func readLock(path string) (*ocamlgen.Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("✗ %s missing (run bob lock)", path)
	}
	l := &ocamlgen.Lock{}
	return l, json.Unmarshal(b, l)
}

// lockCmd: bob lock — resolve opam roots, record versions, source hashes and build order.
func lockCmd(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: bob-ml lock ws.json bob.lock")
	}
	t0 := time.Now()
	w, err := readWS(args[0])
	if err != nil {
		return err
	}
	r := newResolver()
	var roots []opam.Atom
	for _, s := range w.Opam {
		roots = append(roots, opam.Atom{Name: s})
	}
	sol, err := r.Resolve(roots)
	if err != nil {
		return fmt.Errorf("✗ cannot resolve opam packages: %v", err)
	}
	l := &ocamlgen.Lock{Repo: repoCommit(), Packages: map[string]*ocamlgen.Locked{}}
	for n, p := range sol.Pkgs {
		if p.F == nil {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(p.Dir, "opam"))
		h := sha256.Sum256(b)
		e := &ocamlgen.Locked{Name: n, Version: p.Version, OpamHash: hex.EncodeToString(h[:]), Deps: r.BuildDeps(p, sol)}
		if u := p.F.Sections["url"]; u != nil {
			e.URL = opam.StrOf(u["src"])
			if e.URL == "" {
				e.URL = opam.StrOf(u["archive"])
			}
			e.Hash = sri(opam.Items(u["checksum"]))
			if e.Hash == "" {
				return fmt.Errorf("✗ opam:%s.%s has no sha256/sha512 checksum; bob cannot pin it", n, p.Version)
			}
		}
		l.Packages[n] = e
	}
	b, _ := json.MarshalIndent(l, "", " ")
	if err := os.WriteFile(args[1], append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "locked %d opam packages in %.2fs\n", len(l.Packages), time.Since(t0).Seconds())
	return nil
}

func sri(cs []*opam.V) string {
	best := ""
	for _, c := range cs {
		algo, hx, _ := strings.Cut(c.S, "=")
		raw, err := hex.DecodeString(hx)
		if err != nil {
			continue
		}
		s := algo + "-" + base64.StdEncoding.EncodeToString(raw)
		if algo == "sha512" || algo == "sha256" && best == "" {
			best = s
		}
	}
	return best
}
