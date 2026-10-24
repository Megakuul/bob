package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/engine/lock"
)

// TestPinMemo: the pin of an unpinned declaration survives in the state home (A292), per
// declaration, and unreadable memos count as absent.
func TestPinMemo(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "cache", "nixpkgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := readPinMemo(home, lock.DefaultChannel); got.Rev != "" {
		t.Fatalf("empty home: memo %+v, want none", got)
	}
	pin := lock.NixpkgsPin{Channel: lock.DefaultChannel, Rev: "0123456789abcdef0123456789abcdef01234567",
		URL: lock.NixpkgsURL("0123456789abcdef0123456789abcdef01234567"), NarHash: "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	writePinMemo(home, lock.DefaultChannel, pin)
	if got := readPinMemo(home, lock.DefaultChannel); got != pin {
		t.Fatalf("memo = %+v, want %+v", got, pin)
	}
	if got := readPinMemo(home, "nixos-unstable"); got.Rev != "" {
		t.Fatalf("another declaration sees the memo: %+v", got)
	}
	if err := os.WriteFile(pinMemoPath(home, lock.DefaultChannel), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPinMemo(home, lock.DefaultChannel); got.Rev != "" {
		t.Fatalf("broken memo read as %+v, want none", got)
	}
}
