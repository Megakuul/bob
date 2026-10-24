package drv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"unicode/utf8"
)

// normalise turns v into the generic JSON value tree (map[string]any, []any, string, bool,
// json.Number, nil) as a JSON round trip does, so struct tags and json.RawMessage apply.
// Values that already are such a tree (with Go integers) are converted directly, which is
// what derivations mostly hold (A315: the round trip was a fifth of a no-op's CPU); every
// other value takes the round trip, with the same result.
func normalise(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case bool:
		return x, nil
	case string:
		if utf8.ValidString(x) {
			return x, nil
		}
	case int:
		return json.Number(strconv.FormatInt(int64(x), 10)), nil
	case int64:
		return json.Number(strconv.FormatInt(x, 10)), nil
	case int32:
		return json.Number(strconv.FormatInt(int64(x), 10)), nil
	case uint:
		return json.Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(x, 10)), nil
	case uint32:
		return json.Number(strconv.FormatUint(uint64(x), 10)), nil
	case []any:
		if x == nil {
			return nil, nil
		}
		out := make([]any, len(x))
		for i, e := range x {
			n, err := normalise(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case []string:
		if x == nil {
			return nil, nil
		}
		out := make([]any, len(x))
		for i, e := range x {
			n, err := normalise(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case map[string]any:
		if x == nil {
			return nil, nil
		}
		if validKeys(x) {
			out := make(map[string]any, len(x))
			for k, e := range x {
				n, err := normalise(e)
				if err != nil {
					return nil, err
				}
				out[k] = n
			}
			return out, nil
		}
	case map[string]string:
		if x == nil {
			return nil, nil
		}
		if validKeys(x) {
			out := make(map[string]any, len(x))
			for k, e := range x {
				n, err := normalise(e)
				if err != nil {
					return nil, err
				}
				out[k] = n
			}
			return out, nil
		}
	}
	return roundTrip(v)
}

func validKeys[V any](m map[string]V) bool {
	for k := range m {
		if !utf8.ValidString(k) {
			return false
		}
	}
	return true
}

// roundTrip is normalise by encoding/json.
func roundTrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// canonicalJSON serialises v as Nix writes `__json`: keys sorted bytewise, no whitespace,
// only `"`, `\` and control characters escaped (nlohmann::json dump). Only integers are
// accepted as numbers, because Nix prints floats in its own format.
func canonicalJSON(v any) ([]byte, error) {
	n, err := normalise(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := writeJSON(&b, n); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeJSON(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			b.WriteString(strconv.FormatInt(i, 10))
		} else if u, err := strconv.ParseUint(string(x), 10, 64); err == nil {
			b.WriteString(strconv.FormatUint(u, 10))
		} else {
			return fmt.Errorf("number %s is not an integer", x)
		}
	case string:
		return writeJSONString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSONString(b, k); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := writeJSON(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unexpected JSON value %T", v)
	}
	return nil
}

func writeJSONString(b *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("string %q is not valid UTF-8", s)
	}
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return nil
}
