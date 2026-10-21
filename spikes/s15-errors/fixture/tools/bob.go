//go:build ignore

// Illustrative bob.go (S15) for the script packages used by the fault catalogue.
// Each line is one package; the IR records the position of the constructor call.
package tools

import "github.com/megakuul/bob/pkg/step"

var (
	Sleep      = step.Script("sleep", "sleep 30")
	Silent     = step.Script("silent", "echo start; sleep 30")
	Network    = step.Script("network", "exec 3<>/dev/tcp/1.1.1.1/80")
	NoChroot   = step.Script("nochroot", "true", step.Impure())
	Memory     = step.Script("memory", "allocate 2 GB")
	DiskFull   = step.Script("diskfull", "write 64 MB")
	Syntax     = step.Script("syntax", "if then fi")
	Missing    = step.Script("missing-builder", "", step.Builder("bob-builder-missing"))
	NoOutput   = step.Script("no-output", "echo done")
	Base       = step.Script("base", "echo base > $out")
	LeakRef    = step.Script("leak-ref", "echo $DEP_0 > $out", step.Deps(Base), step.Disallow(Base))
	Cross      = step.Script("cross", "true", step.Platform("x86_64-linux"))
	User       = step.Script("user", "read x < $DEP_0; echo $x > $out", step.Deps(Base))
	Payload    = step.Script("payload", "write payload")
	Hook       = step.Script("hook", "echo hooked > $out")
	LogFlood   = step.Script("log-flood", "print 5000 lines")
	Structured = step.Script("structured", "emit structured log lines")
	Lock       = step.Script("lock", "sleep 6")
	Upstream   = step.Script("upstream", "uses nixpkgs hello-s15", step.Nixpkgs("hello-s15"))
)
