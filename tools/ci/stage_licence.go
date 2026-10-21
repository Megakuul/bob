package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/tools/internal/licence"
)

func init() { register(Stage{Name: "licence", Order: 7, Run: runLicence}) }

// licenceModules are the Go modules whose build lists are checked; the
// tools module is checked too because its binaries run in CI.
var licenceModules = []string{".", "tools"}

// attributedDirs hold bundled assets; every file in them needs an
// ATTRIBUTION.md row (AGENTS.md §5.7).
var attributedDirs = []string{"assets", "web/static"}

func runLicence(ctx context.Context, env *Env) error {
	policy, err := licence.LoadPolicy(filepath.Join(env.Root, "tools/ci/licences.allow"))
	if err != nil {
		return err
	}
	var findings []licence.Finding
	for _, m := range licenceModules {
		f, err := licence.CheckGoModule(ctx, filepath.Join(env.Root, m), policy)
		if err != nil {
			return err
		}
		findings = append(findings, f...)
	}
	f, err := licence.CheckNPMLock(filepath.Join(env.Root, "web/package-lock.json"), policy)
	if err != nil {
		return err
	}
	findings = append(findings, f...)

	var bad []string
	for _, f := range findings {
		switch {
		case !f.OK:
			bad = append(bad, fmt.Sprintf("%s@%s: %s", f.Subject, f.Version, f.Reason))
		case f.Reason != "":
			fmt.Printf("licence: %s@%s %s allowed by owner decision %s\n", f.Subject, f.Version, strings.Join(f.Licences, "+"), f.Reason)
		}
	}
	missing, err := licence.CheckAttribution(env.Root, attributedDirs)
	if err != nil {
		return err
	}
	for _, m := range missing {
		bad = append(bad, m+": no row in ATTRIBUTION.md")
	}
	fmt.Printf("licence: %d dependencies checked, %d problems\n", len(findings), len(bad))
	if len(bad) > 0 {
		return fmt.Errorf("licence check failed:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}
