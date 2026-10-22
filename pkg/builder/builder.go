package builder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// Exit codes of bob-builder (SPEC R-NIX-017).
const (
	ExitOK      = 0 // success
	ExitStep    = 1 // a step failed (user or build error)
	ExitInvalid = 2 // invalid job: a bob bug (BOB-E-9004)
	ExitFixup   = 3 // fixup violation (BOB-E-9003)
	ExitOutput  = 4 // a declared output is missing (BOB-E-9002)
)

// maxAttrs caps the structured-attributes file; a job is normally a few KiB, sources never
// travel inside it.
const maxAttrs = 1 << 30

// Kind is one job kind, selected by argv[1].
type Kind struct {
	// Run does the kind's work. A plain error is a failed step (exit 1); errors made with
	// Invalidf are invalid jobs (exit 2).
	Run func(c *Ctx, args json.RawMessage) error
	// NoFixup disables the fixup (R-NIX-019) for kinds whose outputs must stay byte-exact.
	NoFixup bool
}

// Core returns the core kinds of SPEC R-NIX-016. cmd/bob-builder adds the ecosystem kinds of
// pkg/<eco>/bop to this map.
func Core() map[string]Kind {
	return map[string]Kind{
		"run":       {Run: runKind},
		"unpack":    {Run: unpackKind},
		"copy-tree": {Run: copyTreeKind},
	}
}

// exitError carries the exit code of a failure.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// Invalidf reports an invalid job (exit 2): something bob generated wrongly.
func Invalidf(format string, a ...any) error {
	return &exitError{code: ExitInvalid, msg: "invalid job: " + fmt.Sprintf(format, a...)}
}

// Ctx is the context of one job run.
type Ctx struct {
	Job      bspec.Job
	BuildTop string            // NIX_BUILD_TOP (/build in Nix's sandbox)
	Outputs  map[string]string // declared output name → path
	StoreDir string            // NIX_STORE (/nix/store)
	cores    string
	stdout   io.Writer
	stderr   io.Writer
}

// Logf writes one plain log line.
func (c *Ctx) Logf(format string, a ...any) {
	fmt.Fprintf(c.stderr, "bob-builder: "+format+"\n", a...)
}

// Phase announces a phase change to Nix (`@nix {"action":"setPhase",…}`).
func (c *Ctx) Phase(name string) {
	b, _ := json.Marshal(name)
	fmt.Fprintf(c.stderr, "@nix {\"action\":\"setPhase\",\"phase\":%s}\n", b)
}

// Diag writes one structured diagnostic line (`bob:: {"type":"diag",…}`) for bob to translate.
func (c *Ctx) Diag(severity, file string, line, col int, msg string) {
	b, _ := json.Marshal(struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Col      int    `json:"col"`
		Msg      string `json:"msg"`
	}{"diag", severity, file, line, col, msg})
	fmt.Fprintf(c.stderr, "bob:: %s\n", b)
}

// TmpDir is the job's TMPDIR (<build>/tmp), created on first use.
func (c *Ctx) TmpDir() (string, error) {
	d := filepath.Join(c.BuildTop, "tmp")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

// Main runs `bob-builder <kind>` with the given kinds and returns the exit code. It reads only
// $NIX_ATTRS_JSON_FILE and the variables Nix sets (R-NIX-016).
func Main(args []string, kinds map[string]Kind, stdout, stderr io.Writer) (code int) {
	label := "bob-builder"
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "bob-builder: %s: internal error: %v\n", label, r)
			code = ExitInvalid
		}
	}()
	c, kind, err := load(args, kinds, stdout, stderr)
	if c != nil && c.Job.Label != "" {
		label = c.Job.Label
	}
	if err == nil {
		err = kind.Run(c, c.Job.Args)
		if err == nil && !kind.NoFixup {
			c.Phase("fixup")
			err = c.fixup()
		}
		if err == nil {
			err = c.checkOutputs(false)
		}
	}
	if err == nil {
		return ExitOK
	}
	code = ExitStep
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	// The last line names what failed (R-NIX-017); nothing is written after it.
	fmt.Fprintf(stderr, "bob-builder: %s: %s\n", label, oneLine(err.Error()))
	return code
}

func oneLine(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ") }

// attrs is the part of the structured-attributes file the builder reads.
type attrs struct {
	BobSpec json.RawMessage   `json:"bobSpec"`
	Outputs map[string]string `json:"outputs"`
}

// load reads and validates the job.
func load(args []string, kinds map[string]Kind, stdout, stderr io.Writer) (*Ctx, Kind, error) {
	if len(args) != 1 {
		return nil, Kind{}, Invalidf("usage: bob-builder <kind>")
	}
	file := os.Getenv("NIX_ATTRS_JSON_FILE")
	if file == "" {
		return nil, Kind{}, Invalidf("NIX_ATTRS_JSON_FILE is not set (bob derivations use structured attributes)")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, Kind{}, Invalidf("%v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxAttrs+1))
	if err != nil {
		return nil, Kind{}, Invalidf("reading the attributes: %v", err)
	}
	if len(b) > maxAttrs {
		return nil, Kind{}, Invalidf("the attributes file is larger than %d bytes", maxAttrs)
	}
	var a attrs
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, Kind{}, Invalidf("attributes: %v", err)
	}
	c := &Ctx{StoreDir: os.Getenv("NIX_STORE"), cores: os.Getenv("NIX_BUILD_CORES"), stdout: stdout, stderr: stderr,
		Outputs: map[string]string{}}
	if c.StoreDir == "" {
		c.StoreDir = "/nix/store"
	}
	if c.cores == "" {
		c.cores = "1"
	}
	if len(a.BobSpec) == 0 {
		return nil, Kind{}, Invalidf("attribute bobSpec is missing")
	}
	if err := decodeStrict(a.BobSpec, &c.Job); err != nil {
		return nil, Kind{}, Invalidf("bobSpec: %v", err)
	}
	j := &c.Job
	if j.V != 1 {
		return c, Kind{}, Invalidf("job version %d, this builder reads version 1", j.V)
	}
	if j.Kind != args[0] {
		return c, Kind{}, Invalidf("job kind %q does not match the subcommand %q", j.Kind, args[0])
	}
	kind, ok := kinds[j.Kind]
	if !ok || kind.Run == nil {
		return c, Kind{}, Invalidf("unknown job kind %q", j.Kind)
	}
	top := os.Getenv("NIX_BUILD_TOP")
	if top == "" || !filepath.IsAbs(top) {
		return c, Kind{}, Invalidf("NIX_BUILD_TOP %q is not an absolute path", top)
	}
	c.BuildTop = filepath.Clean(top)
	if len(j.Outputs) == 0 {
		return c, Kind{}, Invalidf("the job declares no outputs")
	}
	for _, o := range j.Outputs {
		p, ok := a.Outputs[o]
		if !ok || p == "" || !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
			return c, Kind{}, Invalidf("output %q has no absolute path in the attributes", o)
		}
		if _, dup := c.Outputs[o]; dup {
			return c, Kind{}, Invalidf("output %q declared twice", o)
		}
		c.Outputs[o] = filepath.Clean(p)
	}
	if err := c.checkFixup(); err != nil {
		return c, Kind{}, err
	}
	return c, kind, nil
}

// checkFixup validates the fixup settings before any step runs.
func (c *Ctx) checkFixup() error {
	fx := c.Job.Fixup
	t := fx.Tools
	switch {
	case fx.Strip && t.Strip == "":
		return Invalidf("fixup strips but names no strip tool")
	case fx.DebugOut && (t.Strip == "" || t.Objcopy == ""):
		return Invalidf("fixup moves debug info but names no strip/objcopy tool")
	case fx.DebugOut && !slices.Contains(c.Job.Outputs, "debug"):
		return Invalidf("fixup moves debug info but the job declares no output \"debug\"")
	case fx.ShrinkRPath && t.Patchelf == "":
		return Invalidf("fixup shrinks RUNPATH but names no patchelf tool")
	}
	for _, p := range []string{t.Strip, t.Objcopy, t.Patchelf} {
		if p != "" && !filepath.IsAbs(p) {
			return Invalidf("fixup tool %q is not an absolute path", p)
		}
	}
	for _, p := range fx.Forbidden {
		if _, err := c.hashPart(p); err != nil {
			return err
		}
	}
	return nil
}

// fixupOutput reports whether the fixup itself creates output o (the debug output).
func (c *Ctx) fixupOutput(o string) bool { return o == "debug" && c.Job.Fixup.DebugOut }

// checkOutputs fails with exit 4 when a declared output does not exist. During the steps
// (check-outputs op) outputs the fixup creates are not required yet.
func (c *Ctx) checkOutputs(duringSteps bool) error {
	for _, o := range c.Job.Outputs {
		if duringSteps && c.fixupOutput(o) {
			continue
		}
		if _, err := os.Lstat(c.Outputs[o]); err != nil {
			return &exitError{code: ExitOutput, msg: fmt.Sprintf("declared output %s was not produced (%s)", o, c.Outputs[o])}
		}
	}
	return nil
}

// decodeStrict decodes JSON rejecting unknown fields and trailing data.
func decodeStrict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}
