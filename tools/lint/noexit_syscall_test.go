package lint_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/megakuul/bob/tools/lint"
)

// TestNoExitSyscall: syscall.Exit is flagged like os.Exit, except in the listed init-hook files
// (the namespace child in pkg/engine/store/ns.go, the sandbox stages in
// pkg/engine/eval/evalbox/stage.go); os.Exit stays forbidden there too.
func TestNoExitSyscall(t *testing.T) {
	var pkgs []string
	for _, p := range []string{"internal/y", "pkg/engine/store", "pkg/engine/eval/evalbox", "cmd/tool2"} {
		pkgs = append(pkgs, lint.Module+"/"+p)
	}
	analysistest.Run(t, filepath.Join(analysistest.TestData(), "noexitsys"), lint.NoExit, pkgs...)
}
