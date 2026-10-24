package main

import (
	"context"
	"fmt"

	"github.com/megakuul/bob/tools/internal/protected"
)

func init() { register(Stage{Name: "protected", Order: 1, Run: runProtected}) }

// protectedRef is the branch whose manifest is compared as well, so a
// branch that rewrites files and manifest together is still reported.
const protectedRef = "main"

// runProtected reports changes to protected paths and never fails on them
// (owner decision D68); only a broken manifest or hashing error fails.
func runProtected(ctx context.Context, env *Env) error {
	r, err := protected.Check(ctx, env.Root, protectedRef)
	if err != nil {
		return err
	}
	for _, n := range r.Notes {
		fmt.Println("protected:", n)
	}
	for _, c := range r.Changes {
		fmt.Println("protected:", c)
	}
	fmt.Printf("protected: %d changes to protected paths (reported, not blocked: D68)\n", len(r.Changes))
	return nil
}

// protectWrite backs `go run ./tools/ci protect -write` (HARNESS.md §2.1);
// the runner in main.go dispatches to it.
func protectWrite(ctx context.Context, root string) error { return protected.Write(ctx, root) }
