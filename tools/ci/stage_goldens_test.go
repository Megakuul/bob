package main

import (
	"path/filepath"
	"testing"
)

func TestGoldenStagePasses(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := runGoldens(t.Context(), &Env{Root: root}); err != nil {
		t.Fatal(err)
	}
}
