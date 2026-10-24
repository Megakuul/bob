package jobspec

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestGlob(t *testing.T) {
	for _, c := range []struct {
		p, n string
		ok   bool
	}{{"main", "main", true}, {"main", "mainx", false}, {"*", "feature/x", true}, {"feature*", "feature/a/b", true},
		{"v*", "v1.0.0", true}, {"v?.0", "v1.0", true}, {"v?.0", "v10.0", false}, {"", "", true}, {"a*b*c", "axxbyyc", true}, {"a*b*c", "axxbyy", false}} {
		if Glob(c.p, c.n) != c.ok {
			t.Errorf("Glob(%q, %q) = %v", c.p, c.n, !c.ok)
		}
	}
}

func TestSelectionRoundTrip(t *testing.T) {
	s := Selection{Event: EventTag, Ref: "v1.0.0", Test: true, Variants: []string{"debug"}, Labels: []string{"//a:b"}}
	enc, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSelection(enc)
	if err != nil || got.Event != EventTag || got.Ref != "v1.0.0" || !got.Test || !slices.Equal(got.Labels, s.Labels) {
		t.Fatalf("%v %+v", err, got)
	}
	for _, bad := range [][]string{{"@nope"}, {"@event:push:"}, {"@upload:xyz"}, {"@pipeline:BAD NAME"}, {""}} {
		if _, err := ParseSelection(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := (Selection{Labels: []string{"@x"}}).Encode(); err == nil {
		t.Error("label starting with @ accepted")
	}
}

var ps = []Pipeline{
	{Name: "ci", Triggers: []string{"push main", "pull_request"}, Targets: []string{"//..."}},
	{Name: "release", Triggers: []string{"tag v*"}, Targets: []string{"//cmd:x"}, Releases: []string{"x"}},
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		s    Selection
		want []string
	}{
		{Selection{Event: EventPush, Ref: "main"}, []string{"ci"}},
		{Selection{Event: EventPush, Ref: "dev"}, nil},
		{Selection{Event: EventTag, Ref: "v2"}, []string{"release"}},
		{Selection{Event: EventPullRequest}, []string{"ci"}},
		{Selection{Pipelines: []string{"release"}, Event: EventPush, Ref: "main"}, []string{"release"}},
		{Selection{Labels: []string{"//a:b"}}, nil},
	} {
		got, err := Match(ps, c.s)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("Match(%+v) = %v %v, want %v", c.s, got, err, c.want)
		}
	}
	if _, err := Match(ps, Selection{Pipelines: []string{"nope"}}); !errors.Is(err, ErrNoPipeline) {
		t.Errorf("unknown pipeline: %v", err)
	}
}

func TestReportVerify(t *testing.T) {
	r := &Report{V: 1, Pipelines: ps, Matched: []string{"ci"}, Targets: []string{"//..."}, Tests: []string{}}
	b, _ := json.Marshal(r)
	got, err := ParseReport(b, SHA256(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(Selection{Event: EventPush, Ref: "main"}); err != nil {
		t.Errorf("honest report: %v", err)
	}
	for name, mut := range map[string]func(*Report){
		"added release": func(r *Report) { r.Matched = []string{"ci", "release"} },
		"other targets": func(r *Report) { r.Targets = []string{"//secret:x"} },
		"none matched":  func(r *Report) { r.Matched = nil },
	} {
		f := *got
		mut(&f)
		if err := f.Verify(Selection{Event: EventPush, Ref: "main"}); !errors.Is(err, ErrMatchMismatch) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseReport(b, SHA256([]byte("x"))); err == nil {
		t.Error("sha mismatch accepted")
	}
	bad := []string{`{"v":2}`, `{"v":1,"unknown":1}`, `{"v":1,"packages":[{"label":"a","status":"weird"}]}`,
		`{"v":1,"packages":[{"label":"a","status":"built","deps":[5]}]}`}
	for _, s := range bad {
		if _, err := ParseReport([]byte(s), SHA256([]byte(s))); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
