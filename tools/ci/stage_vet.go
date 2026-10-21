package main

import (
	"context"
	"fmt"
	"strings"
)

// modulePath is bob's module (R-ARCH-001).
const modulePath = "github.com/megakuul/bob"

// allowedTopDirs are where packages of bob's module may live (R-ARCH-001).
// tests/ holds harness packages (HARNESS.md §2), which R-ARCH-001 does not
// list yet; see the M0.2 report.
var allowedTopDirs = []string{"pkg", "cmd", "internal", "proto", "tests"}

func init() {
	register(Stage{Name: "vet", Order: 4, Run: runVet})
}

func runVet(ctx context.Context, env *Env) error {
	if _, err := goCmd(ctx, env.Root, nil, "vet", "./...", "./tools/..."); err != nil {
		return err
	}
	// GOWORK=off keeps the tools module out of the listing (HARNESS.md §1.4).
	out, err := goCmd(ctx, env.Root, []string{"GOWORK=off"}, "list", "./...")
	if err != nil {
		return err
	}
	var bad []string
	for _, p := range strings.Fields(out) {
		if !underAllowedDir(p) {
			bad = append(bad, p)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("R-ARCH-001: packages outside %v:\n%s", allowedTopDirs, strings.Join(bad, "\n"))
	}
	return nil
}

func underAllowedDir(pkg string) bool {
	for _, d := range allowedTopDirs {
		if strings.HasPrefix(pkg, modulePath+"/"+d+"/") || pkg == modulePath+"/"+d {
			return true
		}
	}
	return false
}
