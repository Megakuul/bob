package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
)

// knownEvents are the events of schema v1 (SPEC §02.4) with their required fields and the JSON
// kind of each: "s" string, "n" number, "a" array, "o" object.
func knownEvents() map[string]map[string]string {
	return map[string]map[string]string{
		"run.start":        {"command": "s", "args": "a", "requested": "a", "bob": "s", "nix": "s"},
		"package.start":    {"label": "s", "action": "s", "cache": "s"},
		"package.wait":     {"label": "s", "reason": "s"},
		"upstream.start":   {"name": "s"},
		"log":              {"label": "s", "line": "s"},
		"error":            {"error": "o"},
		"warning":          {"error": "o"},
		"test.result":      {"label": "s", "status": "s", "durationMs": "n"},
		"package.finish":   {"label": "s", "status": "s"},
		"remote.submitted": {"server": "s", "buildId": "s", "url": "s"},
		"remote.state":     {"buildId": "s", "state": "s"},
		"result":           {"command": "s", "data": "o"},
		"run.finish":       {"exitCode": "n", "counts": "o", "drift": "a", "durationMs": "n"},
	}
}

var (
	timeRE = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)
	codeRE = regexp.MustCompile(`^BOB-E-\d{4}$`)
)

func kindOf(v any) string {
	switch v.(type) {
	case string:
		return "s"
	case float64:
		return "n"
	case []any:
		return "a"
	case map[string]any:
		return "o"
	case bool:
		return "b"
	}
	return "?"
}

// validateEvent checks one event against schema v1 and returns the problems found.
func validateEvent(e event, verbose bool) []string {
	var p []string
	if v, ok := e["v"].(float64); !ok || v != 1 {
		p = append(p, fmt.Sprintf("v = %v, want 1", e["v"]))
	}
	name, _ := e["event"].(string)
	fields, known := knownEvents()[name]
	if !known {
		return append(p, fmt.Sprintf("unknown event %q", name))
	}
	if ts, _ := e["time"].(string); !timeRE.MatchString(ts) {
		p = append(p, fmt.Sprintf("%s: time %q is not RFC 3339 UTC with milliseconds", name, ts))
	}
	for f, k := range fields {
		v, ok := e[f]
		if !ok {
			p = append(p, fmt.Sprintf("%s: missing field %q", name, f))
		} else if kindOf(v) != k {
			p = append(p, fmt.Sprintf("%s: field %q has kind %s, want %s", name, f, kindOf(v), k))
		}
	}
	switch name {
	case "package.start":
		if a, _ := e["action"].(string); a != "build" && a != "download" && a != "test" {
			p = append(p, fmt.Sprintf("package.start: action %q", a))
		}
	case "package.finish":
		switch s, _ := e["status"].(string); s {
		case "built", "cached", "failed", "skipped", "cancelled":
		default:
			p = append(p, fmt.Sprintf("package.finish: status %q", s))
		}
	case "test.result":
		switch s, _ := e["status"].(string); s {
		case "pass", "fail", "cached":
		default:
			p = append(p, fmt.Sprintf("test.result: status %q", s))
		}
	case "run.finish":
		counts, _ := e["counts"].(map[string]any)
		for _, k := range []string{"built", "cached", "failed", "skipped", "cancelled", "upstream", "downloaded"} {
			if _, ok := counts[k].(float64); !ok {
				p = append(p, fmt.Sprintf("run.finish: counts.%s missing or not a number", k))
			}
		}
	case "error", "warning":
		obj, _ := e["error"].(map[string]any)
		for f, k := range map[string]string{"code": "s", "severity": "s", "class": "s", "message": "s", "label": "s", "definedAt": "s", "location": "s",
			"cause": "s", "excerpt": "a", "hint": "s", "log": "s", "args": "o"} {
			if v, ok := obj[f]; !ok {
				p = append(p, fmt.Sprintf("%s: error.%s missing", name, f))
			} else if kindOf(v) != k && !(f == "location" && v == nil) {
				p = append(p, fmt.Sprintf("%s: error.%s has kind %s, want %s", name, f, kindOf(v), k))
			}
		}
		if c, _ := obj["code"].(string); !codeRE.MatchString(c) {
			p = append(p, fmt.Sprintf("%s: error.code %q is not BOB-E-NNNN", name, c))
		}
		if s, _ := obj["severity"].(string); s != name {
			p = append(p, fmt.Sprintf("%s: error.severity %q must equal the event name", name, s))
		}
		if _, has := e["raw"]; has && !verbose {
			p = append(p, name+": raw[] present without -v")
		}
	}
	return p
}

// checkStream validates a whole --json run: every line is a schema-v1 event, run.start is first,
// run.finish is last and its exitCode equals the process exit status.
func checkStream(t testing.TB, r harness.Result, verbose bool) []event {
	t.Helper()
	evs := events(t, r.Stdout)
	if len(evs) == 0 {
		t.Errorf("bob %s: no events on stdout (exit %d, stderr %q)", strings.Join(r.Args, " "), r.Exit, r.Stderr)
		return nil
	}
	for i, e := range evs {
		for _, p := range validateEvent(e, verbose) {
			t.Errorf("bob %s: line %d: %s", strings.Join(r.Args, " "), i+1, p)
		}
	}
	if evs[0]["event"] != "run.start" {
		t.Errorf("bob %s: first event is %v, want run.start", strings.Join(r.Args, " "), evs[0]["event"])
	}
	last := evs[len(evs)-1]
	if last["event"] != "run.finish" {
		t.Errorf("bob %s: last event is %v, want run.finish", strings.Join(r.Args, " "), last["event"])
	} else if ec, ok := last["exitCode"].(float64); !ok || int(ec) != r.Exit {
		t.Errorf("bob %s: run.finish exitCode %v != process exit %d", strings.Join(r.Args, " "), last["exitCode"], r.Exit)
	}
	for i, e := range evs[:len(evs)-1] {
		if e["event"] == "run.finish" {
			t.Errorf("bob %s: run.finish at line %d is not the last line", strings.Join(r.Args, " "), i+1)
		}
	}
	// Leak checks cover everything except the two fields that legitimately carry store-like text:
	// package.finish `paths[]` (host paths <state>/nix/store/…, only with --print-paths) and the raw
	// build log lines of `log` events (--json-log).
	var clean strings.Builder
	for _, e := range evs {
		c := event{}
		for k, v := range e {
			c[k] = v
		}
		delete(c, "paths")
		if e["event"] == "log" {
			delete(c, "line")
		}
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("harness: re-encode event: %v", err)
		}
		clean.Write(b)
		clean.WriteByte('\n')
	}
	if strings.Contains(clean.String(), "/nix/store/") {
		t.Errorf("bob %s: --json output names a /nix/store path outside paths[] and log lines", strings.Join(r.Args, " "))
	}
	noLeak(t, harness.Result{Args: r.Args, Stdout: clean.String()})
	return evs
}

// checkSchemaFile checks the protected JSON Schema schema/events-v1.json when it exists: it must
// parse and name every event of v1 (the full validation is validateEvent above).
func checkSchemaFile(t testing.TB) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(harness.Root(t), "schema", "events-v1.json"))
	if err != nil {
		return // reported as a SPEC problem: the schema file is not part of the repository yet
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Errorf("schema/events-v1.json is not JSON: %v", err)
		return
	}
	var names []string
	for n := range knownEvents() {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !strings.Contains(string(b), `"`+n+`"`) {
			t.Errorf("schema/events-v1.json does not mention event %q", n)
		}
	}
}
