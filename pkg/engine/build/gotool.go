package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// evalGoRoot is the Go toolchain of the evaluator (R-FW-043.5, ADR 0003): Options.GoRoot;
// else the go on PATH (the IR does not depend on the Go version, and the eval key records it);
// else nixpkgs' go at bob.lock's pin, or at the default channel's current revision before
// bob.lock has a pin (r.transientPin, which the lock stage then records).
func (r *runner) evalGoRoot(ctx context.Context, st *store.Store, f fetch.Fetcher) (string, error) {
	if r.o.GoRoot != "" {
		return r.o.GoRoot, nil
	}
	if _, err := exec.LookPath("go"); err == nil {
		return "", nil
	}
	lf, err := readLock(r.o.Root)
	decl := lock.DefaultChannel
	switch {
	case err != nil && r.salvageLock:
		// bob lock --rebuild of an unreadable bob.lock: only its nixpkgs revision counts.
		lf = &lock.File{LockVersion: lock.Version}
		raw, _ := os.ReadFile(lockPath(r.o.Root))
		if rev := lock.SalvageNixpkgsRev(raw); rev != "" {
			decl += "@" + rev
		}
	case err != nil:
		return "", err
	}
	pin := lf.Nixpkgs
	if pin.Rev == "" {
		// The channel's current revision is asked every time (update = true); the
		// remembered pin only spares the download that records the narHash when the
		// revision did not move (A292).
		known := readPinMemo(st.Home(), decl)
		if pin, err = lock.PinNixpkgs(ctx, f, decl, known, known.Rev != "", nil); err != nil {
			return "", err
		}
		writePinMemo(st.Home(), decl, pin)
		r.transientPin = pin
	}
	return r.nixpkgsGo(ctx, st, f, pin)
}

// nixpkgsGo realises nixpkgs' go at pin and returns its GOROOT as the host sees it.
//
// An earlier run's answer is remembered per revision (the go memo, A313): when its output is
// valid (checked with Nix, in one batch with the nixpkgs source for the stages after), the
// evaluator's Go costs one Nix call and no index. Otherwise the index resolves go, the source
// is made valid when the index needs it, and go is realised.
func (r *runner) nixpkgsGo(ctx context.Context, st *store.Store, f fetch.Fetcher, pin lock.NixpkgsPin) (string, error) {
	req := lock.NixpkgsRequest(pin)
	src, err := fetch.StorePath(req)
	if err != nil {
		return "", err
	}
	memo := goMemoPath(st.Home(), pin.Rev)
	if out := readGoMemo(memo); out != "" {
		if missing, err := st.Invalid(ctx, []string{src, out}); err == nil && !slices.Contains(missing, out) {
			return path.Join(hostPath(st, out), "share", "go"), nil
		}
	}
	if _, err := f.Ensure(ctx, req); err != nil {
		return "", err
	}
	idx, err := r.hostIndex(ctx, st, src, pin.Rev)
	if err != nil {
		return "", err
	}
	entries, err := idx.Lookup(ctx, []string{"go"})
	if err != nil {
		return "", err
	}
	e := entries["go"]
	out, ok := e.Outputs["out"]
	if e.DrvPath == "" || !ok {
		return "", internalf("nixpkgs:go", "nixpkgs at %s has no go", pin.Rev)
	}
	if missing, err := st.Invalid(ctx, []string{out}); err != nil {
		return "", err
	} else if len(missing) > 0 {
		c := st.Command(ctx, "nix-store", "--realise", e.DrvPath)
		var stderr headTail
		c.Stderr = &stderr
		if err := c.Run(); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// Nix failed to download or build go: a host, network or store problem, not a bob
			// bug (A471). The diagnosis names the cause; -v shows Nix's own text.
			return "", translate.RealiseFailure("nixpkgs:go", stderr.String())
		}
	}
	writeGoMemo(memo, out)
	return path.Join(hostPath(st, out), "share", "go"), nil
}

// goMemoPath is the go memo of nixpkgs revision rev on this system: the output path of go,
// written by bob from its nixpkgs index (the same trust as the index's cache file). A wrong or
// stale memo only costs the batch: the output is used only when Nix reports it valid, and a
// valid store path holds what its name says.
func goMemoPath(home, rev string) string {
	h := sha256.Sum256([]byte(rev + "\x00" + store.HostSystem()))
	return filepath.Join(home, "cache", "nixpkgs", "go-"+hex.EncodeToString(h[:8]))
}

// readGoMemo returns the remembered go output ("" when there is none or it is malformed).
func readGoMemo(file string) string {
	b, err := os.ReadFile(file)
	if err != nil || len(b) > 512 {
		return ""
	}
	out := strings.TrimSpace(string(b))
	if !storePathRe().MatchString(out) {
		return ""
	}
	return out
}

// writeGoMemo is best effort (without it the next run asks the index) and skipped when the
// memo is current.
func writeGoMemo(file, out string) {
	if readGoMemo(file) == out {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".go-")
	if err != nil {
		return
	}
	_, werr := tmp.WriteString(out + "\n")
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// storePathRe is the shape of a store path (the go memo's content).
var storePathRe = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^/nix/store/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._?=-]+$`)
})

// hostIndex opens the host system's nixpkgs index of rev (source src) once per run: the
// evaluator's Go and the index stage share it (its cache file is read once).
func (r *runner) hostIndex(ctx context.Context, st *store.Store, src, rev string) (*nixidx.Index, error) {
	if r.idx != nil && r.idxSrc == src && r.idxRev == rev {
		return r.idx, nil
	}
	idx, err := nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: rev, System: store.HostSystem()})
	if err != nil {
		return nil, err
	}
	r.idx, r.idxSrc, r.idxRev = idx, src, rev
	return idx, nil
}

// The nixpkgs pin memo (A292): a build without a pinned bob.lock (a remote job's fresh source
// snapshot, every time) pins the channel's current revision, which downloads and unpacks the
// whole nixpkgs tree only to record its narHash (lock.PinNixpkgs), about 30 s per build. The
// last pin per declaration is kept below the store's state home; it is used only for the same
// revision; the source is still fetched by that narHash, so a wrong memo can only fail the
// fetch (narHash mismatch), never bring other content.
func pinMemoPath(home, decl string) string {
	h := sha256.Sum256([]byte(decl))
	return filepath.Join(home, "cache", "nixpkgs", "pin-"+hex.EncodeToString(h[:8])+".json")
}

func readPinMemo(home, decl string) lock.NixpkgsPin {
	var p lock.NixpkgsPin
	b, err := os.ReadFile(pinMemoPath(home, decl))
	if err != nil || len(b) > 64<<10 || json.Unmarshal(b, &p) != nil {
		return lock.NixpkgsPin{}
	}
	return p
}

// writePinMemo is best effort: without it the next build downloads again.
func writePinMemo(home, decl string, p lock.NixpkgsPin) {
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	dst := pinMemoPath(home, decl)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pin-")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), dst) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// headTail keeps the first and the last 32 KiB of a Nix stderr: the cache warnings that explain
// a failure come first, the failing step last, and a build log in between can be large.
type headTail struct{ head, tail []byte }

const headTailMax = 32 << 10

func (h *headTail) Write(p []byte) (int, error) {
	n := len(p)
	if room := headTailMax - len(h.head); room > 0 {
		k := min(room, len(p))
		h.head = append(h.head, p[:k]...)
		p = p[k:]
	}
	h.tail = append(h.tail, p...)
	if len(h.tail) > 2*headTailMax {
		h.tail = append([]byte(nil), h.tail[len(h.tail)-headTailMax:]...)
	}
	return n, nil
}

func (h *headTail) String() string {
	t := h.tail
	if len(t) > headTailMax {
		t = t[len(t)-headTailMax:]
	}
	return string(h.head) + string(t)
}
