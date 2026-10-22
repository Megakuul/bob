package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/nar"
)

// streamTree writes a small tree and returns its root, NAR and NAR hash.
func streamTree(t *testing.T) (string, []byte, [32]byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.txt": "alpha\n", "d/b.txt": "beta\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if err := nar.Write(&b, root, nil); err != nil {
		t.Fatal(err)
	}
	sum, _, err := nar.Hash(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return root, b.Bytes(), sum
}

// F5: a streamed item gives the same export stream as the in-memory one.
func TestWriteExportStreamedEqualsInMemory(t *testing.T) {
	root, narBytes, sum := streamTree(t)
	p := sourcePathForTest(t, sum, "streamed")
	var mem, streamed bytes.Buffer
	if err := WriteExport(&mem, []ExportItem{{Path: p, NAR: narBytes}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteExport(&streamed, []ExportItem{TreeItem(p, root, nil, &sum)}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mem.Bytes(), streamed.Bytes()) {
		t.Errorf("streamed export differs from the in-memory export")
	}
}

// F5: a tree that no longer matches its hash aborts the stream before the item is complete.
func TestTreeItemChangedAborts(t *testing.T) {
	root, _, sum := streamTree(t)
	p := sourcePathForTest(t, sum, "streamed")
	sum[0] ^= 0xff
	var out bytes.Buffer
	err := WriteExport(&out, []ExportItem{TreeItem(p, root, nil, &sum)})
	if err == nil || !strings.Contains(err.Error(), "changed while it was imported") {
		t.Fatalf("WriteExport = %v, want a changed-tree error", err)
	}
	if bytes.Contains(out.Bytes(), []byte(p)) {
		t.Errorf("the aborted stream names the path: the item would be complete")
	}
}

// F5: Import streams a large tree into nix-store instead of holding its NAR (twice) in memory,
// and a failing stream leaves the path invalid and reports bob's cause.
func TestImportStreamsLargeTree(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const size = 128 << 20
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "big"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("bob streams\n"), 1<<16)
	for n := 0; n < size; n += len(chunk) {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sum, _, err := nar.Hash(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := sourcePathForTest(t, sum, "bob-stream-test")

	bad := sum
	bad[0] ^= 0xff
	err = s.Import(ctx, []ExportItem{TreeItem(p, root, nil, &bad)})
	if err == nil || !strings.Contains(err.Error(), "changed while it was imported") {
		t.Fatalf("Import of a changed tree = %v, want the changed-tree error", err)
	}
	if inv, err := s.Invalid(ctx, []string{p}); err != nil || len(inv) != 1 {
		t.Fatalf("after the failed import Invalid = %v, %v; want the path invalid", inv, err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := s.Import(ctx, []ExportItem{TreeItem(p, root, nil, &sum)}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
		t.Errorf("Import of a %d MiB tree allocated %d MiB; want it streamed", size>>20, alloc>>20)
	}
	if inv, err := s.Invalid(ctx, []string{p}); err != nil || len(inv) != 0 {
		t.Fatalf("Invalid after import = %v, %v", inv, err)
	}
}
