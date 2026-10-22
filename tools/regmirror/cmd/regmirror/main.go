// Command regmirror serves the offline registry mirror (HARNESS.md §4.3) on loopback. It prints
// "regmirror listening on http://<addr>" once the listener is up and stops on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/megakuul/bob/tools/regmirror"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "regmirror:", err)
		os.Exit(1)
	}
}

func run() error {
	manifests := flag.String("manifests", "tests/fixtures/registry", "manifest directory")
	blobs := flag.String("blobs", "", "blob cache directory")
	listen := flag.String("listen", "127.0.0.1:0", "listen address (loopback)")
	record := flag.Bool("record-crates", false, "fetch missing crates.io index files and .crate files from the network and keep them (manual capture)")
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: regmirror.New(regmirror.Config{Manifests: *manifests, Blobs: *blobs, RecordCrates: *record}), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	fmt.Printf("regmirror listening on http://%s\n", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
