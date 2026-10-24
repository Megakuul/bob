// Command s09 runs the S9 spike experiments against an S3 endpoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"bobspike/s09/internal/store"
)

var (
	endpoint = flag.String("endpoint", "http://127.0.0.1:20900", "S3 endpoint")
	backend  = flag.String("backend", "seaweedfs", "label for output")
	ak       = flag.String("ak", "bobadmin", "access key")
	sk       = flag.String("sk", "bobadmin-secret", "secret key")
)

func conn(ctx context.Context, bucket string, c *store.Counter, attempts int, wipe bool) *store.Conn {
	cn := store.Open(*endpoint, bucket, *ak, *sk, c, attempts)
	if err := cn.EnsureBucket(ctx, wipe); err != nil {
		fmt.Fprintln(os.Stderr, "bucket:", err)
		os.Exit(1)
	}
	return cn
}

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("usage: s09 [flags] probe|panics|crashchild <case>|stress|chaos|poll|lists|faults|oplat")
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "probe":
		err = probe(ctx)
	case "panics":
		err = panics(ctx)
	case "crashchild":
		crashChild(ctx, args[1])
	case "stress":
		err = stress(ctx)
	case "chaos":
		err = chaos(ctx)
	case "poll":
		err = poll(ctx)
	case "lists":
		err = lists(ctx)
	case "faults":
		err = faults(ctx)
	case "oplat":
		err = oplat(ctx)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
