package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Orchestrator key pinning (SPEC R-SRV-102, §05.9.1, S17 §2.2 "cert pin"): every orchestrator
// process has an Ed25519 key, publishes the hex SHA-256 of its SubjectPublicKeyInfo as `fp` in
// fleet/cfg.m, and proves possession of it on every Attach stream: the worker sends a fresh
// nonce in the request header AttachNonceHeader, the orchestrator answers with the header
// AttachProofHeader = base64(SPKI) "." base64(signature over dsAttach || nonce). The proof is
// end to end, so it holds also when a TLS proxy terminates the connection in front of the
// orchestrator. A worker accepts a stream only when the proof verifies against the pin of the
// member it expects at that address.
const (
	AttachNonceHeader = "Bob-Attach-Nonce"
	AttachProofHeader = "Bob-Attach-Proof"
	dsAttach          = "bob-attach-v1"
	attachNonceLen    = 32
	maxProofHeader    = 512
)

// ErrPin: the orchestrator did not prove the pinned key.
var ErrPin = errors.New("orchestrator key does not match its pin")

// ServerKey is an orchestrator's pinned key.
type ServerKey struct {
	priv ed25519.PrivateKey
	spki []byte
}

// NewServerKey creates a fresh key (one per orchestrator process; the pin is republished in
// fleet/cfg.m at every start).
func NewServerKey() (*ServerKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("fleet: server key: %w", err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("fleet: server key: %w", err)
	}
	return &ServerKey{priv: priv, spki: spki}, nil
}

// Pin is the hex SHA-256 of the key's SubjectPublicKeyInfo (fleet/cfg.m `fp`).
func (k *ServerKey) Pin() string { return spkiPin(k.spki) }

func spkiPin(spki []byte) string {
	h := sha256.Sum256(spki)
	return hex.EncodeToString(h[:])
}

// NewAttachNonce returns a fresh nonce for AttachNonceHeader.
func NewAttachNonce() (string, error) {
	b := make([]byte, attachNonceLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("fleet: attach nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func attachMessage(nonce string) ([]byte, error) {
	n, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(n) != attachNonceLen {
		return nil, errors.New("fleet: malformed attach nonce")
	}
	return append([]byte(dsAttach+"\x00"), n...), nil
}

// AttachProof answers a worker's nonce (AttachProofHeader value). The signed message is domain
// separated and fixed-length, so the key signs nothing else through this path.
func (k *ServerKey) AttachProof(nonce string) (string, error) {
	msg, err := attachMessage(nonce)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(k.priv, msg)
	return base64.RawURLEncoding.EncodeToString(k.spki) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyAttachProof checks an AttachProofHeader value for nonce against pin. Proofs are hostile
// input: size-capped, strictly decoded, and the key must be an Ed25519 SPKI hashing to pin.
func VerifyAttachProof(proof, nonce, pin string) error {
	if pin == "" {
		return fmt.Errorf("%w: no pin published", ErrPin)
	}
	if proof == "" || len(proof) > maxProofHeader {
		return fmt.Errorf("%w: no proof", ErrPin)
	}
	k64, s64, ok := strings.Cut(proof, ".")
	if !ok {
		return fmt.Errorf("%w: malformed proof", ErrPin)
	}
	spki, err1 := base64.RawURLEncoding.DecodeString(k64)
	sig, err2 := base64.RawURLEncoding.DecodeString(s64)
	if err1 != nil || err2 != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed proof", ErrPin)
	}
	if subtle.ConstantTimeCompare([]byte(spkiPin(spki)), []byte(strings.ToLower(pin))) != 1 {
		return fmt.Errorf("%w: another key", ErrPin)
	}
	pk, err := x509.ParsePKIXPublicKey(spki)
	pub, isEd := pk.(ed25519.PublicKey)
	if err != nil || !isEd {
		return fmt.Errorf("%w: not an Ed25519 key", ErrPin)
	}
	msg, err := attachMessage(nonce)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return fmt.Errorf("%w: bad signature", ErrPin)
	}
	return nil
}
