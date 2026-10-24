package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// goSourceDirs are the trees gofmt checks. spikes/ holds throwaway Stage 2
// code with its own modules and is not part of bob.
var goSourceDirs = []string{"cmd", "internal", "pkg", "proto", "tests", "tools"}

func init() {
	register(Stage{Name: "fmt", Order: 2, Run: runFmt})
}

func runFmt(ctx context.Context, env *Env) error {
	var dirs []string
	for _, d := range goSourceDirs {
		if _, err := os.Stat(filepath.Join(env.Root, d)); err == nil {
			dirs = append(dirs, d)
		}
	}
	gofmt, err := goCmd(ctx, env.Root, nil, "env", "GOROOT")
	if err != nil {
		return harnessErr(err)
	}
	out, err := command(ctx, env.Root, nil, filepath.Join(strings.TrimSpace(gofmt), "bin", "gofmt"), append([]string{"-l"}, dirs...)...)
	if err != nil {
		return err
	}
	if out = strings.TrimSpace(out); out != "" {
		return fmt.Errorf("gofmt: these files need formatting:\n%s", out)
	}
	out, err = goTool(ctx, filepath.Join(env.Root, "proto"), "buf", "format", "-d")
	if err != nil {
		return err
	}
	if out = strings.TrimSpace(out); out != "" {
		return fmt.Errorf("buf format: proto/ needs formatting:\n%s", out)
	}
	return nil
}
