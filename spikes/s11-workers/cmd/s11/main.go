// Command s11 runs the S11 spike checks against a live S3 server.
//
//	s11 matrix  -server NAME -endpoint URL     # policy matrix (worker vs orchestrator)
//	s11 regflow -server NAME -endpoint URL     # registration flow on dynamitedb
//	s11 sts     -server NAME -endpoint URL     # short-lived credentials
//
// Credentials come from env: S11_ROOT, S11_WORKER, S11_ORCH as "ak:sk".
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"bobspike/s11/internal/s3x"
)

func creds(name, env string) s3x.Creds {
	v := os.Getenv(env)
	ak, sk, _ := strings.Cut(v, ":")
	return s3x.Creds{Name: name, AK: ak, SK: sk}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s11 matrix|regflow|sts|bench ...")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	server := fs.String("server", "server", "server name for output")
	endpoint := fs.String("endpoint", "", "S3 endpoint URL")
	file := fs.String("file", "", "saved STS credentials")
	fs.Parse(os.Args[2:])
	ctx := context.Background()
	root, worker, orch := creds("root", "S11_ROOT"), creds("worker", "S11_WORKER"), creds("orchestrator", "S11_ORCH")
	rc := 0
	switch os.Args[1] {
	case "matrix":
		rc = runMatrix(ctx, *server, *endpoint, root, worker, orch)
	case "regflow":
		rc = runRegflow(ctx, *server, *endpoint, root, worker, orch)
	case "sts":
		rc = runSTS(ctx, *server, *endpoint, root, worker, orch)
	case "stscheck":
		rc = runSTSCheck(ctx, *server, *endpoint, *file)
	case "bpol":
		rc = runBpol(ctx, *server, *endpoint, root, worker, orch)
	case "bench":
		rc = runBench()
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		rc = 2
	}
	if rc != 0 {
		os.Exit(1)
	}
}
