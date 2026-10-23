package fleet

import (
	"errors"
	"strings"
	"testing"
)

func TestAttachProof(t *testing.T) {
	a, err := NewServerKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewServerKey()
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := NewAttachNonce()
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewAttachNonce()
	proof, err := a.AttachProof(nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAttachProof(proof, nonce, a.Pin()); err != nil {
		t.Fatalf("valid proof refused: %v", err)
	}
	if err := VerifyAttachProof(proof, nonce, strings.ToUpper(a.Pin())); err != nil {
		t.Fatalf("upper-case pin refused: %v", err)
	}
	bproof, err := b.AttachProof(nonce)
	if err != nil {
		t.Fatal(err)
	}
	k64, s64, _ := strings.Cut(proof, ".")
	_, bsig, _ := strings.Cut(bproof, ".")
	for name, c := range map[string]struct{ proof, nonce, pin string }{
		"another key (redirected address)": {proof, nonce, b.Pin()},
		"replayed for another nonce":       {proof, other, a.Pin()},
		"no proof":                         {"", nonce, a.Pin()},
		"no pin":                           {proof, nonce, ""},
		"malformed":                        {"x", nonce, a.Pin()},
		"signature of another key":         {k64 + "." + bsig, nonce, a.Pin()},
		"oversized":                        {strings.Repeat("A", 4096), nonce, a.Pin()},
		"truncated signature":              {k64 + "." + s64[:10], nonce, a.Pin()},
	} {
		if err := VerifyAttachProof(c.proof, c.nonce, c.pin); !errors.Is(err, ErrPin) {
			t.Errorf("%s: err %v, want ErrPin", name, err)
		}
	}
	if _, err := a.AttachProof("short"); err == nil {
		t.Error("a malformed nonce was signed")
	}
}
