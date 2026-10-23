package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/translate"
)

// TestUntouchedInvalid: in a run that failed, a requested label Nix reported nothing for is
// `skipped` only when its outputs are not valid in the store (or it has none); a label whose
// outputs are valid is `cached`, so the counts of a failed build do not depend on which
// worker's store was warm (I-B29 NEEDS 2). A run that succeeded needs no check.
func TestUntouchedInvalid(t *testing.T) {
	st := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := filepath.Join(t.TempDir(), "valid.txt")
	if err := os.WriteFile(f, []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := st.Command(ctx, "nix-store", "--add", f)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	valid := strings.TrimSpace(out.String())
	invalidPath := "/nix/store/" + strings.Repeat("0", 32) + "-not-built"

	r := &runner{}
	byLabel := map[string]map[string]string{
		"//a": {"out": valid},
		"//b": {"out": invalidPath},
		"//d": {"out": valid, "doc": invalidPath},
		"//e": {"out": invalidPath},
	}
	status := map[string]string{"//e": "failed"}
	targets := []string{"//a", "//b", "//c", "//d", "//e"}
	res := &Result{Report: &translate.Report{ExitCode: 1}}
	got := r.untouchedInvalid(ctx, st, res, targets, status, byLabel)
	for l, want := range map[string]bool{"//a": false, "//b": true, "//c": true, "//d": true, "//e": false} {
		if got[l] != want {
			t.Errorf("%s invalid = %v, want %v (all: %v)", l, got[l], want, got)
		}
	}
	res.Report.ExitCode = 0
	if got := r.untouchedInvalid(ctx, st, res, targets, status, byLabel); len(got) != 0 {
		t.Errorf("successful run: %v, want no check", got)
	}
}
