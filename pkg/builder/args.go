package builder

import "encoding/json"

// DecodeArgs decodes a kind's Args strictly (unknown fields and trailing data are rejected);
// a failure is an invalid job (exit 2).
func DecodeArgs(raw json.RawMessage, v any) error {
	if err := decodeStrict(raw, v); err != nil {
		return Invalidf("job args: %v", err)
	}
	return nil
}
