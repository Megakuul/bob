package lock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// canonical re-encodes JSON in bob.lock's canonical form (R-FW-004): object keys sorted bytewise,
// arrays in their given order, no HTML escaping, integers only. indent "" gives the compact form.
func canonical(data []byte, indent string) ([]byte, error) {
	v, err := decodeAny(data)
	if err != nil {
		return nil, err
	}
	if err := noFloats(v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := out.Bytes()
	if indent == "" {
		b = bytes.TrimSuffix(b, []byte("\n"))
	}
	return b, nil
}

// decodeAny decodes exactly one JSON value (numbers kept as json.Number).
func decodeAny(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if err := atEOF(dec); err != nil {
		return nil, err
	}
	return v, nil
}

// atEOF fails when the decoder has data left after the value it decoded.
func atEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected data after the JSON value at offset %d", dec.InputOffset())
	}
	return nil
}

// noFloats rejects numbers that are not integers (bob.lock holds no floats).
func noFloats(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			if err := noFloats(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := noFloats(e); err != nil {
				return err
			}
		}
	case json.Number:
		if strings.ContainsAny(string(x), ".eE") {
			return fmt.Errorf("number %s is not an integer", x)
		}
	}
	return nil
}

// marshalCanonical encodes v (a section or entry struct) in the compact canonical form.
func marshalCanonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canonical(b, "")
}

// Inputs returns the "inputs" value of a section: the sha256 (hex) of the canonical JSON of the
// ecosystem's declared requirements (SPEC R-FW-062). decl must encode to JSON; map keys are
// sorted, slices keep their order (sort them first when order does not matter).
func Inputs(decl any) (string, error) {
	b, err := marshalCanonical(decl)
	if err != nil {
		return "", fmt.Errorf("lock: hashing declarations: %w", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
