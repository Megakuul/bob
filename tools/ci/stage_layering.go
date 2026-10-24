package main

import "context"

func init() {
	register(Stage{Name: "layering", Order: 6, Run: runLayering})
}

// runLayering runs TestLayering (R-ARCH-002/003: the layer table and the
// transitive server-code check) and the importboundary analyzer, which
// reports direct boundary violations with file and line.
func runLayering(ctx context.Context, env *Env) error {
	if _, err := goCmd(ctx, env.Root, nil, "test", "-count=1", "-run", "^TestLayering", "./pkg"); err != nil {
		return err
	}
	_, err := goCmd(ctx, env.Root, nil, "run", "./tools/lint/cmd/boblint", "-importboundary", "./...")
	return err
}
