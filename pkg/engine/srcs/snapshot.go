package srcs

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// MaxSnapshotEntries is the entry cap of a source snapshot (SPEC §6.9.7).
const MaxSnapshotEntries = 200_000

// ErrUnsafeSnapshot marks a file a source snapshot must not contain: a symlink whose target is
// absolute or leaves the repository (the worker refuses such snapshots, SPEC §6.9.7).
var ErrUnsafeSnapshot = errors.New("unsafe path in source snapshot")

// Snapshot writes the files (slash-separated, relative to root) as a zstd-compressed tar to w:
// the source snapshot of `bob build --remote` (SPEC §02.6 step 2, §6.9.7). Regular files and
// symlinks are written in sorted order with fixed owner, time and permission bits (0644, or
// 0755 when any execute bit is set), so equal trees give equal bytes and the upload is
// content-addressed. Files that vanished and entries that are neither files nor symlinks
// (directories such as git submodules) are left out. It returns the number of entries written.
func Snapshot(ctx context.Context, root string, files []string, w io.Writer) (int, error) {
	files = slices.Clone(files)
	slices.Sort(files)
	files = slices.Compact(files)
	if len(files) > MaxSnapshotEntries {
		return 0, fmt.Errorf("the repository has %d files; a source snapshot holds at most %d", len(files), MaxSnapshotEntries)
	}
	zw, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return 0, fmt.Errorf("source snapshot: %w", err)
	}
	tw := tar.NewWriter(zw)
	n := 0
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			zw.Close()
			return 0, err
		}
		ok, err := snapshotEntry(tw, root, rel)
		if err != nil {
			zw.Close()
			return 0, err
		}
		if ok {
			n++
		}
	}
	if err := tw.Close(); err != nil {
		zw.Close()
		return 0, fmt.Errorf("source snapshot: %w", err)
	}
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("source snapshot: %w", err)
	}
	return n, nil
}

// snapshotEntry writes one file; ok is false when it was left out.
func snapshotEntry(tw *tar.Writer, root, rel string) (ok bool, err error) {
	clean := path.Clean(rel)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return false, fmt.Errorf("%w: %s", ErrUnsafeSnapshot, rel)
	}
	p := filepath.Join(root, filepath.FromSlash(clean))
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // tracked but deleted in the working tree
	}
	if err != nil {
		return false, fmt.Errorf("source snapshot: %w", err)
	}
	h := &tar.Header{Name: clean, ModTime: time.Unix(0, 0), Mode: 0o644}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return false, fmt.Errorf("source snapshot: %w", err)
		}
		resolved := path.Clean(path.Join(path.Dir(clean), filepath.ToSlash(target)))
		if path.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
			return false, fmt.Errorf("%w: %s -> %s", ErrUnsafeSnapshot, clean, target)
		}
		h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, target, 0o777
		return true, tw.WriteHeader(h)
	case fi.Mode().IsRegular():
		if fi.Mode().Perm()&0o111 != 0 {
			h.Mode = 0o755
		}
		h.Typeflag, h.Size = tar.TypeReg, fi.Size()
		f, err := os.Open(p)
		if err != nil {
			return false, fmt.Errorf("source snapshot: %w", err)
		}
		defer f.Close()
		if err := tw.WriteHeader(h); err != nil {
			return false, fmt.Errorf("source snapshot: %w", err)
		}
		// A file that grows while it is read is cut at the size in its header; one that
		// shrinks fails the snapshot (tar would be corrupt otherwise).
		if _, err := io.Copy(tw, io.LimitReader(f, fi.Size())); err != nil {
			return false, fmt.Errorf("source snapshot: %s: %w", clean, err)
		}
		return true, nil
	}
	return false, nil
}
