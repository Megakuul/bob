package main

import "context"

// Stage is one step of the CI command. Each stage lives in its own file
// (stage_<name>.go) and registers itself from an init function, so tasks
// that add stages in parallel never edit the same file.
type Stage struct {
	Name  string
	Order int  // lower runs first; see HARNESS.md for the stage order
	Slow  bool // only with -slow
	Run   func(ctx context.Context, env *Env) error
}

// Env is what a stage gets from the runner.
type Env struct {
	Root string // repository root
	Fast bool   // -fast: skip expensive parts inside a stage
}

var stages []Stage

func register(s Stage) { stages = append(stages, s) }
