package translate

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// expectation per fault (faults/expect.json): codes in order and the exit code. This is the
// fault catalogue's contract; goldens may be re-blessed only if they still satisfy it.
type expectation struct {
	Codes    []Code `json:"codes"`
	Warnings []Code `json:"warnings"`
	Exit     int    `json:"exit"`
	// ByVersion overrides the expectation for one Nix version (timing races, see write-up)
	ByVersion map[string]expectation `json:"byVersion,omitempty"`
}

func loadExpect(t *testing.T) map[string]expectation {
	b, err := os.ReadFile("../faults/expect.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]expectation
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// leak patterns: raw Nix concepts that must never appear in default output (R-NIX-010/012, BOOTSTRAP §5.5)
var leaks = []*regexp.Regexp{
	regexp.MustCompile(`/nix/store/[0-9a-z]{32}`),
	regexp.MustCompile(`\.drv\b`),
	regexp.MustCompile(`(?i)derivation`),
	regexp.MustCompile(`Cannot build`),
	regexp.MustCompile(`substituter`),
	regexp.MustCompile(`max-jobs|--fallback|trusted-public-keys|__noChroot`),
}

func TestGolden(t *testing.T) {
	exp := loadExpect(t)
	dirs, _ := filepath.Glob("../fixtures/*/*")
	seen := map[string]bool{}
	n := 0
	for _, d := range dirs {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		fault, ver := filepath.Base(filepath.Dir(d)), filepath.Base(d)
		e, ok := exp[fault]
		if !ok {
			t.Errorf("%s: no entry in faults/expect.json", fault)
			continue
		}
		seen[fault] = true
		n++
		if o, ok := e.ByVersion[ver]; ok {
			e = o
		}
		t.Run(fault+"/"+ver, func(t *testing.T) {
			f, stream, err := LoadFixture(d)
			if err != nil {
				t.Fatal(err)
			}
			r, evs := Replay(f, stream)
			var codes []Code
			for _, x := range r.Errors {
				codes = append(codes, x.Code)
			}
			if strings.Join(codeStrs(codes), ",") != strings.Join(codeStrs(e.Codes), ",") {
				t.Errorf("codes = %v, want %v", codes, e.Codes)
			}
			var warns []Code
			for _, x := range r.Warnings {
				warns = append(warns, x.Code)
			}
			if strings.Join(codeStrs(warns), ",") != strings.Join(codeStrs(e.Warnings), ",") {
				t.Errorf("warnings = %v, want %v", warns, e.Warnings)
			}
			if r.ExitCode != e.Exit {
				t.Errorf("exit = %d, want %d", r.ExitCode, e.Exit)
			}
			if len(r.Drift) > 0 {
				t.Errorf("format drift on a supported Nix version: %v", r.Drift)
			}
			plain := HumanString(r, Style{})
			tty := HumanString(r, Style{Color: true, Glyphs: true})
			for _, re := range leaks {
				if loc := re.FindStringIndex(plain); loc != nil {
					t.Errorf("default output leaks Nix detail %q: …%s…", re, plain[max(0, loc[0]-40):min(len(plain), loc[1]+40)])
				}
			}
			if strings.Contains(plain, "\x1b[") {
				t.Errorf("plain output contains ANSI escapes")
			}
			for i := range evs {
				if evs[i].Error != nil {
					c := *evs[i].Error
					c.Raw = nil
					evs[i].Error = &c
				}
			}
			var js bytes.Buffer
			if err := JSONLines(&js, evs); err != nil {
				t.Fatal(err)
			}
			golden(t, filepath.Join(d, "golden.txt"), plain)
			golden(t, filepath.Join(d, "golden.tty.txt"), tty)
			golden(t, filepath.Join(d, "golden.jsonl"), js.String())
		})
	}
	var missing []string
	for k := range exp {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("faults in expect.json without fixtures: %v", missing)
	}
	t.Logf("%d fixtures replayed", n)
}

func codeStrs(cs []Code) []string {
	var out []string
	for _, c := range cs {
		out = append(out, string(c))
	}
	return out
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run go test -update)", path)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

// TestDrift feeds deliberately altered streams: the translator must flag drift instead of
// guessing, and must still fail closed (an error, never a silent success).
func TestDrift(t *testing.T) {
	ctx := &Context{Packages: map[string]Pkg{"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-x.drv": {Label: "//x:x", DefinedAt: "x/bob.go:3:9", Kind: "script"}},
		Outputs: map[string]string{}, Requested: []string{"//x:x"}}
	cases := map[string]struct {
		stream string
		drift  string
		code   Code
	}{
		"unknown activity": {`@nix {"action":"start","id":1,"level":3,"parent":0,"text":"x","type":199,"fields":[]}` + "\n" +
			`@nix {"action":"msg","level":0,"msg":"something new happened"}`, "unknown activity type 199", "BOB-E-9999"},
		"unknown result": {`@nix {"action":"result","id":1,"type":177,"fields":[]}` + "\n" +
			`@nix {"action":"msg","level":0,"msg":"Cannot build '/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-x.drv'.\nReason: builder failed with exit code 1."}`, "unknown result type 177", "BOB-E-2000"},
		"new key":        {`@nix {"action":"msg","level":0,"msg":"x","code":"E123"}`, "unknown JSON keys code", "BOB-E-9999"},
		"garbled":        {`@nix {"action":"msg","lev`, "unparsable @nix line", "BOB-E-9999"},
		"unknown action": {`@nix {"action":"setPhase2","id":1}`, `unknown action "setPhase2"`, "BOB-E-9999"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &Fixture{Context: ctx, Outcome: Outcome{ExitCode: 1, NixVersion: "2.99.0"}}
			r, _ := Replay(f, []byte(c.stream))
			if len(r.Drift) == 0 || !strings.Contains(strings.Join(r.Drift, ";"), c.drift) {
				t.Errorf("drift = %v, want %q", r.Drift, c.drift)
			}
			if len(r.Errors) == 0 || r.Errors[0].Code != c.code {
				t.Errorf("errors = %v, want first %s", r.Errors, c.code)
			}
			if r.ExitCode == 0 {
				t.Errorf("drift must never turn a failed run into success")
			}
		})
	}
}

func TestStyle(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		tty  bool
		env  map[string]string
		want Style
	}{
		{true, nil, Style{Color: true, Glyphs: true}},
		{false, nil, Style{}},
		{true, map[string]string{"NO_COLOR": "1"}, Style{Glyphs: true}},
		{true, map[string]string{"TERM": "dumb"}, Style{}},
		{false, map[string]string{"BOB_COLOR": "always"}, Style{Color: true}},
		{false, map[string]string{"BOB_COLOR": "always", "NO_COLOR": "1"}, Style{}},
	}
	for _, c := range cases {
		if got := StyleFor(c.tty, env(c.env), false); got != c.want {
			t.Errorf("tty=%v env=%v: %+v, want %+v", c.tty, c.env, got, c.want)
		}
	}
}
