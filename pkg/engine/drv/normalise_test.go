package drv

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The direct conversion of normalise gives exactly what the JSON round trip gives (A315).
func TestNormaliseMatchesRoundTrip(t *testing.T) {
	type spec struct {
		Kind string   `json:"kind"`
		Args []string `json:"args,omitempty"`
		N    int      `json:"n"`
	}
	var nilSlice []string
	var nilAny []any
	var nilMap map[string]any
	values := []any{
		nil, true, "plain", "tab\tquote\"<html>&", "bad\xffutf8", 0, -7, int64(1) << 40, uint64(1) << 63,
		int32(-3), uint32(9), 1.0, 2.5, json.Number("12"),
		nilSlice, nilAny, nilMap, []string{"a", "b\xfe"}, []any{1, "x", nil, []any{}, map[string]any{}},
		map[string]string{"k": "v", "bad\xff": "x"}, map[string]string{"a": "b\xff"},
		map[string]any{"outputs": map[string]any{"out": "/nix/store/x"}, "n": 3, "f": 1.0, "s": spec{Kind: "go", N: 2},
			"p": &spec{Args: []string{"-x"}}, "raw": json.RawMessage(`{"b":1,"a":[2]}`), "nil": nilSlice},
		spec{Kind: "k"},
		[]spec{{Kind: "a"}},
	}
	for i, v := range values {
		got, gerr := normalise(v)
		want, werr := roundTrip(v)
		if (gerr == nil) != (werr == nil) || !reflect.DeepEqual(got, want) {
			t.Errorf("value %d (%#v): normalise = %#v, %v; round trip = %#v, %v", i, v, got, gerr, want, werr)
		}
	}
}
