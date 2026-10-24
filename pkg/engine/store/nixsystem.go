//go:build nixsystem

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// systemNix is the absolute path of the static Nix binary in this bob's own Nix closure. The
// flake build (flake.nix, ADR 0002a) sets it with
//
//	-ldflags "-X github.com/megakuul/bob/pkg/engine/store.systemNix=/nix/store/<hash>-nix-static-…/bin/nix"
//
// and compiles with -tags nixsystem, so this bob embeds no Nix. It is a link-time constant: no
// code assigns it (tests aside).
var systemNix string

// The Nix versions a nixsystem bob accepts: the 2.34 series from 2.34.0 on, which bob is
// verified with (the derivation JSON that strategy B writes, the internal-json log format and
// the S15 error captures of 2.34.8 and 2.34.9 the translator replays). Older Nix lacks that
// derivation format; a newer series is unverified. The flake pins 2.34.9 (= NixVersion).
const nixMajor, nixMinor = 2, 34

// nixFlakeHint is the fix for a flake build whose Nix is not the one bob needs.
const nixFlakeHint = "install bob from its flake without overriding its nixpkgs-nix input (no inputs.bob.inputs.nixpkgs-nix.follows), or use the bob AppImage, which embeds its Nix"

// extractNix makes <home>/bin/nix-<h>/bin/nix (with the legacy symlinks) exist and returns its
// bin directory, where h is the first 12 hex of the sha256 of systemNix's path: the same layout
// and flags as the embedded Nix (nixbin.go), but the binary is copied from this bob's Nix
// closure. bob's namespace mounts its private store over /nix, where the host's /nix/store and
// with it systemNix are hidden, so the binary has to live below <home> like the embedded one.
//
// An existing directory is only stat'ed. Otherwise systemNix is checked once (an absolute path,
// a statically linked ELF, `nix --version` reporting Nix in the supported range), copied into a
// temporary directory <home>/bin/.nix-<h>-*, synced and renamed into place atomically; a
// concurrent copy that wins the rename is accepted.
func extractNix(ctx context.Context, home string) (string, error) {
	src := systemNix
	if src == "" || !filepath.IsAbs(src) || filepath.Clean(src) != src {
		return "", diag.New(7003, "what", "this bob was built without the path of its Nix (build it with the flake, which sets it)")
	}
	sum := sha256.Sum256([]byte(src))
	h := hex.EncodeToString(sum[:])
	bin := filepath.Join(home, "bin", "nix-"+h[:12], "bin")
	final := filepath.Dir(bin)
	if fi, err := os.Stat(filepath.Join(bin, "nix")); err == nil && fi.Mode().IsRegular() {
		return bin, nil
	}
	if err := checkSystemNix(ctx, src); err != nil {
		return "", err
	}
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", accessError(err)
	}
	tmp, err := os.MkdirTemp(parent, ".nix-"+h[:12]+"-")
	if err != nil {
		return "", accessError(err)
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", accessError(err)
	}
	if err := copyNix(src, filepath.Join(tmp, "bin")); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		// Another bob process copied the same Nix first.
		if fi, serr := os.Stat(filepath.Join(bin, "nix")); serr == nil && fi.Mode().IsRegular() {
			return bin, nil
		}
		return "", accessError(err)
	}
	if d, err := os.Open(parent); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return bin, nil
}

// prepareNix copies the Nix before bob enters its namespace (store.Enter), where the host's
// /nix/store and with it systemNix are hidden; inside, extractNix then only stats the copy.
func prepareNix(ctx context.Context, home string) error {
	unlock, err := lockState(home)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = extractNix(ctx, home)
	return err
}

// checkSystemNix refuses a systemNix that bob's namespace cannot run or that bob does not
// support: missing, dynamically linked (its libraries live in the host's /nix/store, which the
// namespace hides), Lix, or a Nix outside the 2.34 series.
func checkSystemNix(ctx context.Context, src string) error {
	f, err := elf.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return diag.New(7003, "what", "the Nix this bob was built with is missing: "+src)
		}
		return diag.New(1026, "what", fmt.Sprintf("the Nix this bob was built with is not a Linux executable (%s)", src), "hint", nixFlakeHint)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			f.Close()
			return diag.New(1026, "what", fmt.Sprintf("the Nix this bob was built with is dynamically linked (%s); bob needs a statically linked Nix (nixStatic)", src), "hint", nixFlakeHint)
		}
	}
	f.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, src, "--version")
	cmd.Env = []string{"PATH=" + filepath.Dir(src), "HOME=/nonexistent"}
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		d := diag.New(1026, "what", fmt.Sprintf("the Nix this bob was built with does not run (%s --version failed)", src), "hint", nixFlakeHint)
		d.Raw = []string{err.Error()}
		return d
	}
	impl, ver, err := parseNixVersion(out.String())
	if err != nil {
		d := diag.New(1026, "what", fmt.Sprintf("the Nix this bob was built with reports no version bob understands (%s)", src), "hint", nixFlakeHint)
		d.Raw = []string{strings.TrimSpace(out.String())}
		return d
	}
	if impl != "Nix" {
		return diag.New(1026, "what", fmt.Sprintf("this bob was built with %s %s, but bob needs Nix (Lix lacks the derivation features bob uses)", impl, fmtVersion(ver)), "hint", nixFlakeHint)
	}
	if ver[0] != nixMajor || ver[1] != nixMinor {
		return diag.New(1026, "what", fmt.Sprintf("this bob was built with Nix %s, but bob needs Nix %d.%d.x (at least %d.%d.0)", fmtVersion(ver), nixMajor, nixMinor, nixMajor, nixMinor), "hint", nixFlakeHint)
	}
	return nil
}

// parseNixVersion reads the first line of `nix --version`: "nix (Nix) 2.34.9" or
// "nix (Lix, like Nix) 2.93.3". Suffixes after the patch number ("2.35.0pre…") are ignored.
func parseNixVersion(out string) (impl string, ver [3]int, err error) {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	lp, rp := strings.Index(line, "("), strings.Index(line, ")")
	if lp < 0 || rp < lp {
		return "", ver, fmt.Errorf("store: unexpected nix --version output %q", line)
	}
	impl, _, _ = strings.Cut(line[lp+1:rp], ",")
	f := strings.Fields(line[rp+1:])
	if len(f) == 0 {
		return "", ver, fmt.Errorf("store: unexpected nix --version output %q", line)
	}
	parts := strings.SplitN(f[0], ".", 3)
	if len(parts) < 2 {
		return "", ver, fmt.Errorf("store: unexpected Nix version %q", f[0])
	}
	for i, p := range parts {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		if end == 0 || (i < 2 && end != len(p)) {
			return "", ver, fmt.Errorf("store: unexpected Nix version %q", f[0])
		}
		n, err := strconv.Atoi(p[:end])
		if err != nil {
			return "", ver, fmt.Errorf("store: unexpected Nix version %q", f[0])
		}
		ver[i] = n
	}
	return strings.TrimSpace(impl), ver, nil
}

func fmtVersion(v [3]int) string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

// copyNix copies src to dir/nix (mode 0555, synced) and adds the legacy links.
func copyNix(src, dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return accessError(err)
	}
	in, err := os.Open(src)
	if err != nil {
		return accessError(err)
	}
	defer in.Close()
	path := filepath.Join(dir, "nix")
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return accessError(err)
	}
	_, cerr := io.Copy(out, in)
	if cerr == nil {
		cerr = out.Sync()
	}
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return accessError(cerr)
	}
	if err := os.Chmod(path, 0o555); err != nil {
		return accessError(err)
	}
	for _, l := range nixLinks {
		if err := os.Symlink("nix", filepath.Join(dir, l)); err != nil {
			return accessError(err)
		}
	}
	return nil
}
