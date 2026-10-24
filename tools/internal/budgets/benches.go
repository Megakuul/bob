package budgets

// Benches maps a budget row id to the command that measures it. The command
// runs from the repository root and prints one line "BUDGET <row id> <value>"
// per row it measured, with the value in the row's unit and statistic.
//
// A row without an entry has no benchmark yet and does not fail the run. The
// task that ships the feature adds the entry and regenerates the budgets file.
//
// tools/budgetbench documents each group (fixture, statistic, runs). Rows that
// share a command are measured by one invocation. Not registered (I-B38): B-038
// (bob has no separate measurement of the node_modules materialisation; a whole
// no-op build is only an upper bound), B-083 (an installed shim needs a release
// registry; tests/install TestRINST002ShimOverhead asserts it), B-084 (label
// completion, [2c/4G] only).
var Benches = map[string]string{
	"B-001": "go run ./tools/budgetbench eval1000",
	"B-007": "go run ./tools/budgetbench noop-polyglot",
	"B-009": "go run ./tools/budgetbench noop-go",
	"B-010": "go run ./tools/budgetbench sizes",
	"B-011": "go run ./tools/budgetbench sizes",
	"B-014": "go run ./tools/budgetbench dev",
	"B-016": "go run ./tools/budgetbench noop-go",
	"B-018": "go run ./tools/budgetbench dev",
	"B-019": "go run ./tools/budgetbench dev",
	"B-021": "go run ./tools/budgetbench noop-go",
	"B-029": "go run ./tools/budgetbench noop-rust",
	"B-037": "go run ./tools/budgetbench npm-lock",
	"B-045": "go run ./tools/budgetbench noop-ocaml",
	"B-049": "go run ./tools/budgetbench codegen",
	"B-052": "go run ./tools/budgetbench codegen",
	"B-053": "go run ./tools/budgetbench noop-cc",
	"B-057": "go run ./tools/budgetbench fleet-jobs",
	"B-058": "go run ./tools/budgetbench fleet-idle",
	"B-068": "go run ./tools/budgetbench api",
	"B-070": "go run ./tools/budgetbench api",
	"B-075": "go run ./tools/budgetbench evalbox",
	"B-079": "go run ./tools/budgetbench dev",
	"B-080": "go run ./tools/budgetbench dev",
	"B-081": "go run ./tools/budgetbench dev",
	"B-087": "go run ./tools/budgetbench appimage",
	"B-088": "go run ./tools/budgetbench fleet-jobs",
	"B-097": "go run ./tools/budgetbench ui",
}
