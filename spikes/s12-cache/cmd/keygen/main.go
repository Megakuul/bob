// Command keygen writes a Nix-format ed25519 signing key pair (Go implementation) or the
// sha256 of a token for the server's token file.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"bobspike/s12/nixfmt"
)

func main() {
	name := flag.String("name", "bob-s12-1", "key name")
	out := flag.String("out", "", "secret key file; public key goes to <out>.pub")
	hash := flag.String("hash-token", "", "print sha256 hex of a token and exit")
	flag.Parse()
	if *hash != "" {
		s := sha256.Sum256([]byte(*hash))
		fmt.Println(hex.EncodeToString(s[:]))
		return
	}
	k, err := nixfmt.GenerateKey(*name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(k.String()), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.WriteFile(*out+".pub", []byte(k.Public().String()), 0o644)
	fmt.Println(k.Public())
}
