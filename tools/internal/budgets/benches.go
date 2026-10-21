package budgets

// Benches maps a budget row id to the command that measures it. The command
// runs from the repository root and prints one line "BUDGET <row id> <value>"
// per row it measured, with the value in the row's unit and statistic.
//
// A row without an entry has no benchmark yet and does not fail the run. The
// task that ships the feature adds the entry and regenerates the budgets file.
var Benches = map[string]string{}
