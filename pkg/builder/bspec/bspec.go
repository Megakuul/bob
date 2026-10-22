package bspec

import "encoding/json"

// BuilderVersion is bob-builder's own version (SPEC R-NIX-015). It is bumped only when the
// builder's behaviour changes (2: test-run timeout, A92; unpack symlink rules, F1; 3: the Go
// kinds of pkg/golang/bop; 4: test-run Copy; 5: the C/C++ kinds of pkg/cc/bop, test-run format
// tap; 6: the npm kinds of pkg/npm/bop, Ctx.Unpack/Copy; 7: the OCaml kinds of
// pkg/ocaml/bop; Go cross env, variant flags and the -asan compiler shim of pkg/golang/bop). The CI stage "builder" fails when the builder changed since the last commit
// without a bump.
const BuilderVersion = 7

// Job is one derivation's work. Kind selects the subcommand; Args is the kind's own schema.
type Job struct {
	V       int             `json:"v"`       // 1
	Kind    string          `json:"kind"`    // must equal argv[1]
	Label   string          `json:"label"`   // for log lines
	Outputs []string        `json:"outputs"` // "out", optional "dev", "lib", "debug"
	Args    json.RawMessage `json:"args"`
	Fixup   Fixup           `json:"fixup"`
}

// Fixup is the fixup contract of one job (SPEC R-NIX-019).
type Fixup struct {
	Strip       bool     `json:"strip"`       // false for profile debug
	DebugOut    bool     `json:"debugOut"`    // move debug info to the "debug" output
	ShrinkRPath bool     `json:"shrinkRpath"` // patchelf --shrink-rpath
	Forbidden   []string `json:"forbidden"`   // store paths that must not be referenced
	Tools       Tools    `json:"tools"`       // store paths of strip, objcopy, patchelf
}

// Tools are the store paths of the binaries fixup runs.
type Tools struct {
	Strip    string `json:"strip"`
	Objcopy  string `json:"objcopy"`
	Patchelf string `json:"patchelf"`
}

// RunArgs is the Args schema of kind "run" (bob.Run): steps run in order.
type RunArgs struct {
	Steps []Step `json:"steps"`
}

// Step is one operation of a "run" job. Op is one of mkdir, copy, symlink, write, exec and
// check-outputs; Args is the op's schema (the types below).
type Step struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

// MkdirArgs creates Path and its parents.
type MkdirArgs struct {
	Path string `json:"path"`
}

// CopyArgs copies the file or tree From to To.
type CopyArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SymlinkArgs creates the symlink Link pointing at Target.
type SymlinkArgs struct {
	Target string `json:"target"`
	Link   string `json:"link"`
}

// WriteArgs writes Content to Path with the permission bits Mode (default 0644).
type WriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode,omitempty"`
}

// ExecArgs runs Argv[0] (looked up in Path) with exactly the environment of SPEC R-NIX-016
// plus Env, in Cwd.
type ExecArgs struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
	Path []string          `json:"path,omitempty"`
	// TimeoutS stops the program after that many seconds (0 = no limit of its own); the step
	// then fails with the diagnostic "<label> timed out after <n>s".
	TimeoutS int64 `json:"timeoutSeconds,omitempty"`
}

// CheckFilesArgs requires every path of Paths (relative, below Root) to exist: bob.Run's
// declared outputs. A missing one fails the job (exit 4) with the diagnostic
// "<label>: step finished but did not create <path>".
type CheckFilesArgs struct {
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
}

// UnpackArgs is the Args schema of kind "unpack": unpack Archive into To, dropping Strip
// leading path components.
type UnpackArgs struct {
	Archive string `json:"archive"`
	To      string `json:"to"`
	Strip   int    `json:"strip"`
}

// CopyTreeArgs is the Args schema of kind "copy-tree".
type CopyTreeArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}
