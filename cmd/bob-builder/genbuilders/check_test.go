package main

import (
	"strings"
	"testing"
)

func table(arm, amd string) string {
	return `package store

func builderTable(system string) string {
	switch system {
	case "aarch64-linux":
		return "` + arm + `"
	case "x86_64-linux":
		return "` + amd + `"
	}
	return ""
}
`
}

const (
	arm1  = "/nix/store/ihm0v89fl3gv8whblg2fad8pdwqvs6p4-bob-builder-1"
	amd1  = "/nix/store/4d3bqpb9vcv7xp4ng5absg9pkabg7rw5-bob-builder-1"
	arm1b = "/nix/store/0000000000000000000000000000000a-bob-builder-1"
	amd1b = "/nix/store/0000000000000000000000000000000b-bob-builder-1"
	arm2  = "/nix/store/0000000000000000000000000000000c-bob-builder-2"
	amd2  = "/nix/store/0000000000000000000000000000000d-bob-builder-2"
)

// F18 / R-NIX-015: the CI check fails on a stale table and on a builder change without a
// BuilderVersion bump; a bump, an unchanged builder or no previous commit pass.
func TestCheckTables(t *testing.T) {
	tests := []struct {
		name                    string
		computed, working, base string
		wantErr                 string
	}{
		{"unchanged", table(arm1, amd1), table(arm1, amd1), table(arm1, amd1), ""},
		{"first commit", table(arm1, amd1), table(arm1, amd1), "", ""},
		{"bumped", table(arm2, amd2), table(arm2, amd2), table(arm1, amd1), ""},
		{"no bump", table(arm1b, amd1b), table(arm1b, amd1b), table(arm1, amd1), "bspec.BuilderVersion is still 1"},
		{"one system changed", table(arm1, amd1b), table(arm1, amd1b), table(arm1, amd1), "x86_64-linux"},
		{"stale", table(arm1b, amd1b), table(arm1, amd1), table(arm1, amd1), "stale"},
	}
	for _, tt := range tests {
		err := checkTables(tt.computed, tt.working, tt.base)
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("%s: error %v, want one containing %q", tt.name, err, tt.wantErr)
		}
	}
}
