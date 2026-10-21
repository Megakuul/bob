package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

// Tokens are signed with the orchestrator key from the control bucket (S11 okey/<kid>),
// which workers cannot read. Domain separation keeps worker tokens and peer tokens apart.

type peerClaims struct {
	Iss string `json:"iss"` // calling orchestrator id
	Aud string `json:"aud"` // target orchestrator id
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

type workerClaims struct {
	Wid    string   `json:"wid"`
	Builds []string `json:"b"` // stand-in for "builds of jobs this worker holds a lease on"
	Exp    int64    `json:"exp"`
}

var b64 = base64.RawURLEncoding

func sign(priv ed25519.PrivateKey, prefix, domain string, claims any) string {
	c, _ := json.Marshal(claims)
	sig := ed25519.Sign(priv, append([]byte(domain+"\x00"), c...))
	return prefix + b64.EncodeToString(c) + "." + b64.EncodeToString(sig)
}

func verify(pub ed25519.PublicKey, tok, prefix, domain string, out any) error {
	if len(tok) > 2048 || !strings.HasPrefix(tok, prefix) {
		return errors.New("wrong token type")
	}
	parts := strings.Split(strings.TrimPrefix(tok, prefix), ".")
	if len(parts) != 2 {
		return errors.New("malformed token")
	}
	c, err1 := b64.DecodeString(parts[0])
	s, err2 := b64.DecodeString(parts[1])
	if err1 != nil || err2 != nil || !ed25519.Verify(pub, append([]byte(domain+"\x00"), c...), s) {
		return errors.New("bad signature")
	}
	return json.Unmarshal(c, out)
}

func peerToken(priv ed25519.PrivateKey, iss, aud string) string {
	now := time.Now().Unix()
	return sign(priv, "bpt1.", "bob-peer-v1", peerClaims{iss, aud, now, now + 60})
}

func checkPeer(pub ed25519.PublicKey, tok, self string, isMember func(string) bool) (string, error) {
	var c peerClaims
	if err := verify(pub, tok, "bpt1.", "bob-peer-v1", &c); err != nil {
		return "", err
	}
	now := time.Now().Unix()
	switch {
	case c.Aud != self:
		return "", errors.New("audience mismatch")
	case c.Exp < now || c.Iat > now+60 || c.Exp-c.Iat > 300:
		return "", errors.New("expired")
	case !isMember(c.Iss):
		return "", errors.New("issuer is not a live orchestrator")
	}
	return c.Iss, nil
}

func workerToken(priv ed25519.PrivateKey, wid string, builds []string) string {
	return sign(priv, "bwt1.", "bob-wtok-v1", workerClaims{wid, builds, time.Now().Add(time.Hour).Unix()})
}

func checkWorker(pub ed25519.PublicKey, tok, build string) (string, error) {
	var c workerClaims
	if err := verify(pub, tok, "bwt1.", "bob-wtok-v1", &c); err != nil {
		return "", err
	}
	if c.Exp < time.Now().Unix() {
		return "", errors.New("expired")
	}
	for _, b := range c.Builds {
		if b == build {
			return c.Wid, nil
		}
	}
	return "", errors.New("worker holds no job of this build")
}

// selfCert makes a throwaway TLS cert; peers pin its SPKI hash from the orch/ record.
func selfCert() (tls.Certificate, string) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bob-orchestrator"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{"localhost"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	c, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: c}, spkiFP(c)
}

func spkiFP(c *x509.Certificate) string {
	h := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(h[:])
}

// pinnedTLS accepts exactly the certificate whose SPKI hash is fp.
func pinnedTLS(fp string) *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || spkiFP(cs.PeerCertificates[0]) != fp {
			return errors.New("peer certificate does not match orch record pin")
		}
		return nil
	}}
}
