//go:build !online

package main

import (
	"context"
	"errors"
)

// snapshot needs the network and bob's store: go run -tags online ./cmd/nixpkgsgen snapshot.
func snapshot(context.Context, string, string, string, []string) error {
	return errors.New("snapshot is online; run: go run -tags online ./cmd/nixpkgsgen snapshot")
}
