package srcs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/store"
)

// TestImportManyTiming imports 10,000 small source dirs (budget B-076: <= 5 s) and logs the
// phases; a second import of the same sources imports nothing.
func TestImportManyTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	in := make([]Source, 10000)
	for i := range in {
		root := filepath.Join(base, fmt.Sprint(i))
		if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "src", "a.c"), []byte(fmt.Sprintf("int a%d;\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		in[i] = Source{Name: fmt.Sprintf("many-%d-src", i), Tree: Tree{Root: root, Files: []string{"src/a.c"}}}
	}
	start := time.Now()
	if _, err := Import(ctx, st, in); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	start = time.Now()
	if _, err := Import(ctx, st, in); err != nil {
		t.Fatal(err)
	}
	t.Logf("10,000 dirs: cold import %v, warm (all valid) %v", cold, time.Since(start))
	if cold > 5*time.Second {
		t.Errorf("cold import took %v, budget 5 s", cold)
	}
}
