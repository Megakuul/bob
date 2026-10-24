package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// redTolerated lets a test that fails with "red: not implemented yet" pass the stage. It holds
// until Gate H is recorded as passed (AGENTS.md §3, Phase 3.1): the harness exists, the
// features are stubs, and the acceptance tests are red for that one reason. Flip it to false
// at the first feature milestone that starts turning tests green (AGENTS.md §5.2: the red
// markers may then no longer hide a failure).
const redTolerated = true

// redMarkers start the failure text of a test that is red only because a stub answered
// BOB-E-9008 (tests/harness.Red, harness.RedFeature; the UI harness says "not implemented:").
var redMarkers = []string{"red: not implemented yet", "not implemented:"}

func hasRedMarker(s string) bool {
	for _, m := range redMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func init() {
	register(Stage{Name: "acceptance", Order: 13, Run: runAcceptance})
}

type testKey struct{ pkg, test string }

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

// runAcceptance runs the acceptance suites (HARNESS.md §14 stage 13). Per test it records
// whether it failed, and whether every failure line carried the red marker. A package that does
// not build, a panic, a timeout or a failure without the marker fails the stage. The parity
// catalogue is fed by parity.Covers in the tests themselves.
func runAcceptance(ctx context.Context, env *Env) error {
	pkgs, err := goPackages(ctx, env)
	if err != nil {
		return err
	}
	var acc []string
	for _, p := range pkgs {
		if isAcceptance(p) {
			acc = append(acc, p)
		}
	}
	if len(acc) == 0 {
		fmt.Println("acceptance: no suites yet")
		return nil
	}
	args := append([]string{"test", "-json", "-count=1", "-timeout", "30m"}, acc...)
	if env.Fast {
		args = append(args, "-short")
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = env.Root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acceptance: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("acceptance: %w", err)
	}
	out := map[testKey]*strings.Builder{}
	failed := map[testKey]bool{}
	pkgFailed := map[string]bool{}
	pkgOut := map[string]*strings.Builder{}
	passed := 0
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e testEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		k := testKey{e.Package, e.Test}
		switch e.Action {
		case "output":
			m := pkgOut
			if e.Test != "" {
				if out[k] == nil {
					out[k] = &strings.Builder{}
				}
				out[k].WriteString(e.Output)
				continue
			}
			if m[e.Package] == nil {
				m[e.Package] = &strings.Builder{}
			}
			m[e.Package].WriteString(e.Output)
		case "fail":
			if e.Test != "" {
				failed[k] = true
			} else {
				pkgFailed[e.Package] = true
			}
		case "pass":
			if e.Test != "" {
				passed++
			}
		}
	}
	if err := cmd.Wait(); err != nil && len(failed) == 0 && len(pkgFailed) == 0 {
		return fmt.Errorf("go test (acceptance): %w\n%s", err, stderr.String())
	}
	var real, red []string
	for k := range failed {
		o := ""
		if out[k] != nil {
			o = out[k].String()
		}
		if !hasOwnFailureLine(o) && hasFailedSubtest(failed, k.pkg, k.test) {
			continue // a parent fails because a subtest does; the subtests are judged themselves
		}
		if redTolerated && hasRedMarker(o) && onlyRedFailures(o) {
			red = append(red, k.pkg+"."+k.test)
			continue
		}
		real = append(real, fmt.Sprintf("--- FAIL %s %s\n%s", k.pkg, k.test, tail(o, 25)))
	}
	// A package that failed without failing test: build error, panic, timeout, TestMain.
	for p := range pkgFailed {
		hasTest := false
		for k := range failed {
			if k.pkg == p {
				hasTest = true
			}
		}
		if !hasTest {
			real = append(real, fmt.Sprintf("--- FAIL package %s\n%s", p, tail(pkgOut[p].String(), 25)))
		}
	}
	sort.Strings(real)
	fmt.Printf("acceptance: %d packages, %d tests pass, %d red (not implemented), %d fail\n",
		len(acc), passed, len(red), len(real))
	if len(real) > 0 {
		return fmt.Errorf("%d acceptance failures that are not \"not implemented\":\n%s",
			len(real), strings.Join(real, "\n"))
	}
	return nil
}

// onlyRedFailures reports whether every test failure line (those with "_test.go:N:" that
// come from t.Error/Fatal) carries the red marker.
func onlyRedFailures(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "===") || l == "" || l == "FAIL" || strings.HasPrefix(l, "PASS") {
			continue
		}
		if !strings.Contains(l, ".go:") { // continuation lines of a multi-line message
			continue
		}
		if !hasRedMarker(l) {
			return false
		}
	}
	return true
}

func hasOwnFailureLine(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, ".go:") && !strings.HasPrefix(strings.TrimSpace(l), "---") {
			return true
		}
	}
	return false
}

func hasFailedSubtest(failed map[testKey]bool, pkg, test string) bool {
	for k := range failed {
		if k.pkg == pkg && strings.HasPrefix(k.test, test+"/") {
			return true
		}
	}
	return false
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
