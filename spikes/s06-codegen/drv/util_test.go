package drv

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

func readFile(p string) (string, error) { b, err := os.ReadFile(p); return string(b), err }
func hexs(h [32]byte) string            { return hex.EncodeToString(h[:]) }
func sha(s string) [32]byte             { return sha256.Sum256([]byte(s)) }
