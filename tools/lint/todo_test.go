package lint

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// A finding in a file listed in dbusage.todo is expected, not reported.
func TestDBUsageTodo(t *testing.T) {
	old := dbusageTodo
	defer func() { dbusageTodo = old }()
	dbusageTodo = "import internal/legacy/legacy.go listed\ncall internal/legacy/legacy.go listed\n"
	analysistest.Run(t, filepath.Join(analysistest.TestData(), "dbusage"), DBUsage, Module+"/internal/legacy")
}
