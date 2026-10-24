package ext

import (
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

// R-FW-046: reading an axis the kind did not declare is an error naming the kind and the axis;
// declared axes return the node's configuration.
func TestUndeclaredAxis(t *testing.T) {
	var got []string
	old := graph.Misuse
	graph.Misuse = func(msg string) { got = append(got, msg) }
	t.Cleanup(func() { graph.Misuse = old })

	c := Config{c: graph.Config{Kind: "toy.bad", Axes: []int{graph.AxisPlatform}, Platform: "linux-arm64", Profile: "debug", Race: true}}
	if p := c.Platform(); p != bob.LinuxARM64 {
		t.Errorf("Platform() = %q", p)
	}
	if p := c.Profile(); p != "" {
		t.Errorf("Profile() of an undeclared axis = %q, want the zero value", p)
	}
	_ = c.Race()
	_ = c.Sanitizers()
	_ = c.Features(bob.Label{Name: "x"})
	want := []string{
		"ext: kind toy.bad reads the profile but did not declare AxisProfile",
		"ext: kind toy.bad reads the Go race setting but did not declare AxisGoRace",
		"ext: kind toy.bad reads the sanitizers but did not declare AxisSanitizers",
		"ext: kind toy.bad reads the Rust features but did not declare AxisFeatures",
	}
	if len(got) != len(want) {
		t.Fatalf("misuse messages %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegisterLookup(t *testing.T) {
	Register(Kind{Name: "exttest.thing", Axes: []Axis{AxisProfile, AxisPlatform, AxisProfile}, Host: true})
	k, ok := Lookup("exttest.thing")
	if !ok || !k.Host || len(k.Axes) != 2 || k.Axes[0] != AxisPlatform || k.Axes[1] != AxisProfile {
		t.Errorf("Lookup = %+v, %v", k, ok)
	}
	if _, ok := Lookup("exttest.none"); ok {
		t.Error("an unregistered kind was found")
	}
	if _, ok := Lookup("bob.run"); !ok {
		t.Error("the core kind bob.run is not registered")
	}
	before := len(graph.Problems())
	Register(Kind{Name: "Bad Name"})
	Register(Kind{Name: "exttest.thing", Axes: []Axis{AxisPlatform}})
	if n := len(graph.Problems()) - before; n != 2 {
		t.Errorf("%d registration problems recorded, want 2: %q", n, graph.Problems())
	}
}

// New outside an evaluation gives a zero Node and records the misuse.
func TestNewOutsideEvaluation(t *testing.T) {
	n := New(nil, "bob.run", "x", nil, nil, nil)
	if n.Label() != (bob.Label{}) {
		t.Errorf("New outside evaluation = %+v", n.Label())
	}
}
