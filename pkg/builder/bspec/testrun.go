package bspec

// TestRunArgs is the Args schema of kind "test-run" (SPEC §03.0.5): start the services in
// order, run the test binary once, write $out/test-result.json and $out/test.log. Every
// ecosystem's test unit ends in one such job; the engine recognises test units by this kind.
type TestRunArgs struct {
	Argv []string          `json:"argv"`           // the test binary (looked up in Path) and its args
	Path []string          `json:"path,omitempty"` // PATH of the test, in order (tool bin dirs)
	Env  map[string]string `json:"env,omitempty"`  // added to the test's environment
	Cwd  string            `json:"cwd,omitempty"`  // working directory (default: the build dir)
	// Copy is copied (in order, before the services start) into writable paths, typically the
	// package's source tree into the build dir so the test runs in it with Cwd there (Go tests
	// read testdata/ relative to their package directory).
	Copy     []CopyArgs `json:"copy,omitempty"`
	Format   string     `json:"format,omitempty"`   // per-case parser: "" (none) or "go" (-test.v output)
	Services []Service  `json:"services,omitempty"` // R-FW-025, started in list order
	// TimeoutS stops the test after that many seconds (0 = DefaultTestTimeoutS); the unit then
	// fails with the diagnostic "<label> timed out after <n>s" (BOB-E-2007).
	TimeoutS int64 `json:"timeoutSeconds,omitempty"`
}

// DefaultTestTimeoutS is the timeout of a test unit that sets none: 10 minutes (A92).
const DefaultTestTimeoutS = 600

// Service is one loopback service of a test (R-FW-025). Instance i (from 0) gets the address
// 127.0.0.(i+2): it is exported as ADDR to the service and its probe, as <NAME>_ADDR to the
// test, and "$ADDR" in Argv and Env values is replaced by it.
type Service struct {
	Name      string            `json:"name"`           // [A-Za-z0-9._-], ≤ 64; used in errors and <NAME>_ADDR
	Argv      []string          `json:"argv"`           // argv[0] is looked up in Path
	Path      []string          `json:"path,omitempty"` // the bin dirs of the service's tools
	Env       map[string]string `json:"env,omitempty"`  // exported to the test (after $ADDR expansion)
	Ready     Probe             `json:"ready"`          // readiness probe
	TimeoutMs int64             `json:"timeoutMs"`      // readiness timeout (> 0)
}

// Probe is a readiness probe: exactly one of Port (TCP connect to ADDR:Port succeeds) and Argv
// (the command exits 0; run with the service's PATH and ADDR) is set.
type Probe struct {
	Port int      `json:"port,omitempty"`
	Argv []string `json:"argv,omitempty"`
}

// TestResult is $out/test-result.json of every test-run derivation (schema version 1).
type TestResult struct {
	Version  int        `json:"version"` // 1
	Label    string     `json:"label"`
	Passed   bool       `json:"passed"`
	Duration float64    `json:"durationSeconds"`
	Cases    []TestCase `json:"cases"` // empty if the framework gives no per-case data
}

// TestCase is one case of a TestResult.
type TestCase struct {
	Name     string  `json:"name"`
	Result   string  `json:"result"` // "pass" | "fail" | "skip"
	Duration float64 `json:"durationSeconds"`
	Output   string  `json:"output,omitempty"` // failing cases only, ≤ 64 KiB each
}
