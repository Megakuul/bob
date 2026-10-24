package drv

import "testing"

// The placeholder of "out" as Nix prints it (`builtins.placeholder "out"`).
func TestPlaceholder(t *testing.T) {
	if got := Placeholder("out"); got != "/1rz4g4znpzjwh1xymhjpm42vipw92pr73vdgl6xs1hycac8kf2n9" {
		t.Errorf("Placeholder(out) = %s", got)
	}
}
