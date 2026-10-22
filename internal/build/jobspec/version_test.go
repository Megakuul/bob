package jobspec

import "testing"

func TestSelectionVersion(t *testing.T) {
	s := Selection{Event: EventTag, Ref: "v1.4.2", Version: "1.4.2"}
	enc, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSelection(enc)
	if err != nil || got.Version != "1.4.2" {
		t.Fatalf("%v → %+v %v", enc, got, err)
	}
	if _, err := (Selection{Version: "-x"}).Encode(); err == nil {
		t.Errorf("invalid version encoded")
	}
	for _, bad := range [][]string{{"@version:"}, {"@version:a b"}, {"@version:1", "@version:2"}} {
		if _, err := ParseSelection(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	for tag, want := range map[string]string{"v1.4.2": "1.4.2", "1.0.0": "1.0.0", "v1.6.0-rc.1": "1.6.0-rc.1", "release/v1": "", "v": ""} {
		if got := TagVersion(tag); got != want {
			t.Errorf("TagVersion(%q) = %q, want %q", tag, got, want)
		}
	}
}
