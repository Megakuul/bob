package cli

import (
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
)

// runBuildCmd is `bob build`: plan only with --dry-run (R-RS-005, ruling G24), else the build.
func runBuildCmd(e *env) int {
	if e.a.bool("dry-run") {
		return runDryRun(e)
	}
	return runBuild(e)
}

// runDryRun evaluates and plans the patterns without fetching, importing or building anything
// and prints the planned derivations, one store path per line, sorted. The plan needs no
// network and no crate sources: bob.lock carries every fact it uses (R-RS-005).
func runDryRun(e *env) int {
	if e.a.bool("remote") {
		return e.out.fail(diag.New(4, "flag", "--dry-run", "what", "a local build (not --remote)"))
	}
	if err := checkPlatforms(e.a.str("platform"), e.a.pos); err != nil {
		return e.out.fail(err)
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	opts, err := e.buildOptions(root, build.CmdBuild)
	if err != nil {
		return e.out.fail(err)
	}
	opts.DryRun, opts.PrintPaths = true, false
	st, caches, warns, exit, done := e.openStore(true)
	if done {
		return exit
	}
	opts.Store, opts.Caches = st, caches
	for _, w := range warns {
		e.out.diagnose(w, "")
	}
	res, err := build.Run(e.ctx, opts)
	if res == nil {
		return e.out.fail(err)
	}
	errs := res.Report.Errors
	if err != nil {
		errs = append(errs, diagErrors(err)...)
	}
	if len(errs) > 0 || res.ExitCode != 0 {
		for _, d := range errs {
			e.out.diagnose(d, "")
		}
		exit := diag.ExitCode(append(append([]*diag.Error{}, errs...), e.out.errs...))
		if exit == 0 {
			exit = res.ExitCode
		}
		return e.out.finish(exit, res.Report.Counts, nil)
	}
	// Human output only: the event schema (schema/events-v1.json) has no plan event yet.
	for _, p := range res.Planned {
		e.out.printf("%s\n", p)
	}
	return e.out.finish(0, res.Report.Counts, nil)
}
