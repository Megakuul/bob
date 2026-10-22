// Package serverenv is the shared base of the server test environment
// (HARNESS.md §8): it finds the repository, reads tools/pins.json, fetches pinned
// non-Go tools into the harness tool cache, realises nixpkgs-pinned tools through
// the host Nix, and hands out loopback ports.
//
// The sub-packages build on it: rustfs (launcher), s3count (counting and
// fault-injecting S3 proxy), s3sig (SigV4 for harness clients), chaos (kill -9
// driver), forgefix (recorded forge payloads), forgejo and gitlab (live forges)
// and clients (real nix and podman conformance runners).
//
// A missing pinned tool is a harness error, never a skipped test (HARNESS.md §2):
// callers turn ErrHarness into exit status 2.
package serverenv

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// ErrHarness marks failures of the harness itself (a pinned tool that cannot be
// fetched or started), as opposed to failures of the code under test.
var ErrHarness = errors.New("harness error")

// Port range reserved for the server test environment.
const (
	PortMin = 23000
	PortMax = 23999
)

// Root returns the repository root: the nearest ancestor of the working
// directory whose go.mod declares the bob module.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("serverenv: getwd: %w", err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.HasPrefix(string(b), "module github.com/megakuul/bob\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("serverenv: no bob go.mod above the working directory: %w", ErrHarness)
		}
		dir = parent
	}
}

// Pins is tools/pins.json.
type Pins struct {
	V       int               `json:"v"`
	Nixpkgs string            `json:"nixpkgs"`
	Tools   map[string]Tool   `json:"tools"`
	Nix     map[string]string `json:"nix"` // harness name → nixpkgs attribute
}

// Tool is one downloaded (non-nixpkgs) tool.
type Tool struct {
	Version   string              `json:"version"`
	Platforms map[string]Artifact `json:"platforms"` // GOOS/GOARCH → artifact
}

// Artifact is one pinned download.
type Artifact struct {
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Extract string `json:"extract"` // member of a zip archive; empty for a plain binary
}

// LoadPins reads tools/pins.json from the repository root.
func LoadPins() (*Pins, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(root, "tools", "pins.json"))
	if err != nil {
		return nil, fmt.Errorf("serverenv: read pins: %w: %w", err, ErrHarness)
	}
	var p Pins
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("serverenv: parse pins: %w: %w", err, ErrHarness)
	}
	return &p, nil
}

// CacheDir is the harness cache, ~/.cache/bob-harness (XDG_CACHE_HOME honoured).
func CacheDir() (string, error) {
	c, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("serverenv: cache dir: %w: %w", err, ErrHarness)
	}
	return filepath.Join(c, "bob-harness"), nil
}

// fetchMu serialises fetches inside one test binary; separate binaries are safe
// because files are published with an atomic rename.
var fetchMu sync.Mutex

// ToolPath returns the path of the pinned tool name for this platform. It looks
// in tools/<sha256>/ of the cache first. Unless offline, it downloads the
// artifact, checks size and sha256 and extracts it. Gate runs are offline
// (HARNESS.md §4.3), so the cache must be warm; an archive placed in the cache
// directory by hand is accepted when its hash matches.
func ToolPath(ctx context.Context, name string, offline bool) (string, error) {
	pins, err := LoadPins()
	if err != nil {
		return "", err
	}
	t, ok := pins.Tools[name]
	if !ok {
		return "", fmt.Errorf("serverenv: tool %q is not pinned: %w", name, ErrHarness)
	}
	plat := runtime.GOOS + "/" + runtime.GOARCH
	a, ok := t.Platforms[plat]
	if !ok {
		return "", fmt.Errorf("serverenv: tool %q has no pin for %s: %w", name, plat, ErrHarness)
	}
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "tools", a.SHA256)
	bin := filepath.Join(dir, name)
	if fi, err := os.Stat(bin); err == nil && fi.Mode()&0o111 != 0 {
		return bin, nil
	}
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	// Parallel test binaries (go test ./...) race here: serialise them with an
	// flock on the tool directory, then look again.
	unlock, err := lockDir(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	if fi, err := os.Stat(bin); err == nil && fi.Mode()&0o111 != 0 {
		return bin, nil
	}
	archive := filepath.Join(dir, filepath.Base(a.URL))
	if err := verify(archive, a); err != nil {
		if offline {
			return "", fmt.Errorf("serverenv: %s not in the tool cache (%s) and the run is offline: %w", name, archive, ErrHarness)
		}
		if err := download(ctx, a, archive); err != nil {
			return "", err
		}
	}
	if err := extract(archive, a.Extract, bin); err != nil {
		return "", err
	}
	return bin, nil
}

// lockDir takes an exclusive flock on dir/.lock; the returned func releases it.
func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("serverenv: flock %s: %w: %w", dir, err, ErrHarness)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// tempFile creates a uniquely named temp file next to dst (mode perm).
func tempFile(dst string, perm os.FileMode) (*os.File, error) {
	f, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".part-*")
	if err != nil {
		return nil, err
	}
	return f, f.Chmod(perm)
}

func verify(path string, a Artifact) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 || (a.Size > 0 && n != a.Size) {
		return fmt.Errorf("serverenv: %s: sha256 %s size %d, pinned %s size %d", path, got, n, a.SHA256, a.Size)
	}
	return nil
}

func download(ctx context.Context, a Artifact, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("serverenv: fetch %s: %w: %w", a.URL, err, ErrHarness)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("serverenv: fetch %s: %s: %w", a.URL, resp.Status, ErrHarness)
	}
	f, err := tempFile(dst, 0o644)
	if err != nil {
		return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	_, cerr := io.Copy(f, io.LimitReader(resp.Body, a.Size+1))
	if err := errors.Join(cerr, f.Close()); err != nil {
		return fmt.Errorf("serverenv: fetch %s: %w: %w", a.URL, err, ErrHarness)
	}
	if err := verify(tmp, a); err != nil {
		return fmt.Errorf("%w: %w", err, ErrHarness)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	return nil
}

func extract(archive, member, bin string) error {
	if member == "" {
		src, err := os.ReadFile(archive)
		if err != nil {
			return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
		}
		out, err := tempFile(bin, 0o755)
		if err != nil {
			return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
		}
		defer os.Remove(out.Name())
		_, werr := out.Write(src)
		if err := errors.Join(werr, out.Close()); err != nil {
			return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
		}
		return publish(out.Name(), bin)
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("serverenv: open %s: %w: %w", archive, err, ErrHarness)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != member {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
		}
		out, err := tempFile(bin, 0o755)
		if err != nil {
			rc.Close()
			return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
		}
		tmp := out.Name()
		defer os.Remove(tmp)
		_, cerr := io.Copy(out, rc)
		if err := errors.Join(cerr, rc.Close(), out.Close()); err != nil {
			return fmt.Errorf("serverenv: extract %s: %w: %w", member, err, ErrHarness)
		}
		return publish(tmp, bin)
	}
	return fmt.Errorf("serverenv: %s has no member %q: %w", archive, member, ErrHarness)
}

// publish atomically moves tmp to dst; an existing valid dst (another process
// won the race) is fine.
func publish(tmp, dst string) error {
	if err := os.Rename(tmp, dst); err != nil {
		if fi, serr := os.Stat(dst); serr == nil && fi.Mode()&0o111 != 0 {
			return nil
		}
		return fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	return nil
}

// NixTool realises the nixpkgs attribute pinned under name through the host Nix
// and returns its store path. A GC root under the harness cache keeps it alive.
func NixTool(ctx context.Context, name string) (string, error) {
	pins, err := LoadPins()
	if err != nil {
		return "", err
	}
	attr, ok := pins.Nix[name]
	if !ok {
		return "", fmt.Errorf("serverenv: nix tool %q is not pinned: %w", name, ErrHarness)
	}
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	link := filepath.Join(cache, "nix", name)
	if p, err := filepath.EvalSymlinks(link); err == nil {
		// The .attr file records the pinned reference, so a pin change rebuilds.
		if b, err := os.ReadFile(link + ".attr"); err == nil && string(b) == pins.Nixpkgs+"#"+attr {
			return p, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	ref := pins.Nixpkgs + "#" + attr
	cmd := exec.CommandContext(ctx, "nix", "--extra-experimental-features", "nix-command flakes",
		"build", "--out-link", link, "--print-out-paths", ref)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("serverenv: nix build %s: %w: %s: %w", ref, err, ee.Stderr, ErrHarness)
		}
		return "", fmt.Errorf("serverenv: nix build %s: %w: %w", ref, err, ErrHarness)
	}
	if err := os.WriteFile(link+".attr", []byte(ref), 0o644); err != nil {
		return "", fmt.Errorf("serverenv: %w: %w", err, ErrHarness)
	}
	// A multi-output attribute prints one path per output; the first is the
	// one requested (e.g. openssl.bin).
	return strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0], nil
}

// FreePort returns a loopback port in [PortMin, PortMax] that was free when
// probed. Random probing keeps parallel suites from colliding on the same port.
func FreePort() (int, error) {
	for range 200 {
		p := PortMin + rand.IntN(PortMax-PortMin+1)
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err != nil {
			continue
		}
		if err := l.Close(); err != nil {
			continue
		}
		return p, nil
	}
	return 0, fmt.Errorf("serverenv: no free port in %d–%d: %w", PortMin, PortMax, ErrHarness)
}
