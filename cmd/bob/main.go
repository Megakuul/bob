// Command bob is the bob command line.
package main

import (
	"crypto/x509"
	"os"

	"github.com/megakuul/bob/pkg/cli"
	"github.com/megakuul/bob/pkg/engine/store"
)

func main() {
	// Hosts without a CA bundle (minimal container images) still verify TLS: Go uses these
	// roots only when the system has none (A242).
	if roots := x509.NewCertPool(); roots.AppendCertsFromPEM(store.CABundle()) {
		x509.SetFallbackRoots(roots)
	}
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
