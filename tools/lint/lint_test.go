package lint_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/megakuul/bob/tools/lint"
)

// Each analyzer has its own GOPATH tree under testdata/<name> with packages
// that must be flagged (// want) and packages that must stay clean.
func TestAnalyzers(t *testing.T) {
	tests := []struct {
		a    *analysis.Analyzer
		pkgs []string
	}{
		{lint.NoPanic, []string{"internal/x", "cmd/tool", "pkg/bob/diag"}},
		{lint.NoExit, []string{"internal/x", "cmd/tool"}},
		{lint.NoList, []string{"internal/x", "internal/store/sweep"}},
		{lint.ImportBoundary, []string{"internal/x", "internal/store", "pkg/cc", "cmd/bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.a.Name, func(t *testing.T) {
			var pkgs []string
			for _, p := range tt.pkgs {
				pkgs = append(pkgs, lint.Module+"/"+p)
			}
			analysistest.Run(t, filepath.Join(analysistest.TestData(), tt.a.Name), tt.a, pkgs...)
		})
	}
}
