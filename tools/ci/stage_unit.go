package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Suites are the acceptance packages of HARNESS.md §14; everything else under tests/ is
// harness infrastructure and runs in the unit stage together with pkg/, internal/, cmd/ and
// tools/. A package belongs to a suite when its path below tests/ starts with one of these.
var acceptanceDirs = []string{
	"core", "nix", "cli", "install", "devenv", "outputs", "platforms", "eco",
	"errors", "determinism", "fixtures", "rootless", "sandbox",
	"store", "policy", "server", "chaos", "soak", "rbac", "auth", "forge", "cache", "oci", "ui",
	"bundles",
}

func isAcceptance(pkg string) bool {
	rest, ok := strings.CutPrefix(pkg, modulePath+"/tests/")
	if !ok {
		return false
	}
	for _, d := range acceptanceDirs {
		if rest == d || strings.HasPrefix(rest, d+"/") {
			return true
		}
	}
	return false
}

func init() {
	register(Stage{Name: "unit", Order: 10, Run: runUnit})
}

// runUnit is `go test -race -shuffle=on -count=1` over everything that is not an acceptance
// suite (HARNESS.md §1.3 stage 10). -race needs cgo, and this host has no C compiler, so it is
// used only when one is on PATH; the report says which mode ran.
func runUnit(ctx context.Context, env *Env) error {
	pkgs, err := goPackages(ctx, env)
	if err != nil {
		return err
	}
	var unit []string
	for _, p := range pkgs {
		if !isAcceptance(p) {
			unit = append(unit, p)
		}
	}
	args := []string{"test", "-shuffle=on", "-count=1"}
	mode := "no race detector (no C compiler on PATH)"
	var extra []string
	if !env.Fast {
		if _, err := exec.LookPath("gcc"); err == nil {
			args = append(args, "-race")
			extra = []string{"CGO_ENABLED=1"}
			mode = "-race"
		}
	} else {
		args = append(args, "-short")
		mode = "-short"
	}
	fmt.Printf("unit: %d packages, %s\n", len(unit), mode)
	out, err := goCmd(ctx, env.Root, extra, append(args, unit...)...)
	if err != nil {
		return fmt.Errorf("go test (unit): %w\n%s", err, out)
	}
	return nil
}

// goPackages lists the packages of bob's module and of the tools module.
func goPackages(ctx context.Context, env *Env) ([]string, error) {
	out, err := goCmd(ctx, env.Root, nil, "list", "./...", "./tools/...")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}
