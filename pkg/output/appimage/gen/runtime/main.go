// Command bobrun is the runtime of bob's AppImages (SPEC R-OUT-015, R-OUT-016): a static Go
// program (no libfuse, no FUSE) with a squashfs image appended. It extracts the image once into
// a per-user cache and starts the bundled program in a private user and mount namespace with
// the bundled store at /nix/store ("userns"), or through the bundled dynamic loader ("ldso").
//
// It is its own Go module so that its dependency graph is exactly what ships inside every
// bundle (the standard library and github.com/klauspost/compress, BSD-3-Clause). bob embeds
// prebuilt binaries of it for both platforms (pkg/output/appimage/gen, SPEC O-04-2).
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// version is the runtime's own version, printed by --appimage-version.
const version = "bobrun 1"

func main() {
	if s := os.Getenv(stage2Env); s != "" {
		os.Exit(stage2(s))
	}
	os.Exit(run(os.Args))
}

// fail prints a launcher error and returns its exit code.
func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "bobrun: "+format+"\n", a...)
	return 127
}

func run(args []string) int {
	self, err := os.Executable()
	if err != nil {
		return fail("cannot find the AppImage file: %v", err)
	}
	f, err := os.Open(self)
	if err != nil {
		return fail("cannot open the AppImage file: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fail("cannot read the AppImage file: %v", err)
	}
	off, err := elfEnd(f)
	if err != nil {
		return fail("%s is not an ELF file: %v", self, err)
	}
	embedded := off < fi.Size()
	if len(args) > 1 && strings.HasPrefix(args[1], "--appimage-") {
		return flags(args[1:], f, off, fi.Size(), embedded)
	}
	var dir string
	if embedded {
		if dir, err = cached(f, off, fi.Size()); err != nil {
			return fail("%v", err)
		}
	} else {
		// AppRun of an extracted AppDir: the bundle is next to the binary.
		dir = filepath.Dir(self)
	}
	appimage := ""
	if embedded {
		appimage = self
	}
	return launch(dir, appimage, args)
}

func flags(args []string, f *os.File, off, size int64, embedded bool) int {
	switch args[0] {
	case "--appimage-offset":
		fmt.Println(off)
		return 0
	case "--appimage-version":
		fmt.Println(version)
		return 0
	case "--appimage-help":
		fmt.Print(`Usage: <bundle>.AppImage [arguments of the program]

bob AppImage runtime options (only as the first argument):
  --appimage-extract [<glob>]  extract the bundle (or the paths matching glob) to ./squashfs-root
  --appimage-offset            print the offset of the squashfs image
  --appimage-version           print the runtime version
  --appimage-help              print this help

Environment:
  BOB_ENTRY=<name>             run another entry of the bundle
  BOB_APPIMAGE_MODE=auto|userns|ldso
  BOB_APPIMAGE_DEBUG=1         print the launcher's choices
`)
		return 0
	case "--appimage-extract":
		if !embedded {
			return fail("this file has no squashfs image")
		}
		glob := ""
		if len(args) > 1 {
			glob = strings.TrimPrefix(path.Clean("/"+args[1]), "/")
			if _, err := path.Match(glob, ""); err != nil {
				return fail("bad pattern %q: %v", args[1], err)
			}
		}
		dst := "squashfs-root"
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return fail("%v", err)
		}
		s, err := openSquashfs(io.NewSectionReader(f, off, size-off), size-off)
		if err != nil {
			return fail("%v", err)
		}
		defer s.close()
		if err := s.extract(dst, globKeep(glob)); err != nil {
			return fail("extract: %v", err)
		}
		return 0
	}
	return fail("unknown option %s (see --appimage-help)", args[0])
}

// globKeep selects the entries matching glob, the directories leading to them and everything
// below a matching directory ("" selects everything).
func globKeep(glob string) func(string, bool) bool {
	if glob == "" {
		return nil
	}
	return func(rel string, dir bool) bool {
		if ok, _ := path.Match(glob, rel); ok {
			return true
		}
		for p := path.Dir(rel); p != "."; p = path.Dir(p) {
			if ok, _ := path.Match(glob, p); ok {
				return true
			}
		}
		if dir {
			// a parent of a possible match: compare the leading segments
			gs, rs := strings.Split(glob, "/"), strings.Split(rel, "/")
			if len(rs) < len(gs) {
				for i, r := range rs {
					if ok, _ := path.Match(gs[i], r); !ok {
						return false
					}
				}
				return true
			}
		}
		return false
	}
}

// elfEnd returns where the ELF file ends: the end of its section header table or of its
// furthest segment, whichever is later. The squashfs image starts there.
func elfEnd(r io.ReaderAt) (int64, error) {
	var h [64]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return 0, err
	}
	if string(h[:4]) != "\x7fELF" || h[4] != 2 || h[5] != 1 {
		return 0, errors.New("not a little-endian ELF64 file")
	}
	le := binary.LittleEndian
	phoff, shoff := int64(le.Uint64(h[32:])), int64(le.Uint64(h[40:]))
	phentsize, phnum := int64(le.Uint16(h[54:])), int64(le.Uint16(h[56:]))
	shentsize, shnum := int64(le.Uint16(h[58:])), int64(le.Uint16(h[60:]))
	end := shoff + shentsize*shnum
	if phentsize < 56 || phnum > 128 {
		return 0, errors.New("bad program headers")
	}
	ph := make([]byte, phentsize*phnum)
	if _, err := r.ReadAt(ph, phoff); err != nil {
		return 0, err
	}
	for i := int64(0); i < phnum; i++ {
		p := ph[i*phentsize:]
		if e := int64(le.Uint64(p[8:])) + int64(le.Uint64(p[32:])); e > end {
			end = e
		}
	}
	// Go puts the section headers first and .shstrtab last, outside every segment.
	if shentsize < 64 || shnum > 1024 {
		return 0, errors.New("bad section headers")
	}
	sh := make([]byte, shentsize*shnum)
	if _, err := r.ReadAt(sh, shoff); err != nil {
		return 0, err
	}
	for i := int64(0); i < shnum; i++ {
		s := sh[i*shentsize:]
		if le.Uint32(s[4:]) == 8 { // SHT_NOBITS
			continue
		}
		if e := int64(le.Uint64(s[24:])) + int64(le.Uint64(s[32:])); e > end {
			end = e
		}
	}
	return end, nil
}

// cacheBases lists the directories that may hold extracted bundles, in order:
// $XDG_CACHE_HOME/bob/appimage, ~/.cache/bob/appimage, $TMPDIR/bob-appimage-<uid> (R-OUT-015).
func cacheBases() []string {
	var out []string
	if x := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		out = append(out, filepath.Join(x, "bob", "appimage"))
	}
	if h := os.Getenv("HOME"); filepath.IsAbs(h) {
		out = append(out, filepath.Join(h, ".cache", "bob", "appimage"))
	}
	tmp := os.Getenv("TMPDIR")
	if !filepath.IsAbs(tmp) {
		tmp = "/tmp"
	}
	return append(out, filepath.Join(tmp, fmt.Sprintf("bob-appimage-%d", os.Getuid())))
}

const completeMarker = ".complete"

// cached returns the directory of the extracted bundle, extracting it first if needed. A
// bundle is extracted into a temporary directory next to its final place and renamed when
// complete, so concurrent first runs leave exactly one directory.
func cached(f *os.File, off, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, size)); err != nil {
		return "", fmt.Errorf("read the AppImage file: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	var errs []string
	for _, base := range cacheBases() {
		dir := filepath.Join(base, sum)
		if _, err := os.Stat(filepath.Join(dir, completeMarker)); err == nil {
			return dir, nil
		}
		if err := extractTo(f, off, size, base, sum); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("cannot extract the bundle: %s", strings.Join(errs, "; "))
}

func extractTo(f *os.File, off, size int64, base, sum string) error {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(base, "."+sum[:16]+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // nothing left after a rename
	s, err := openSquashfs(io.NewSectionReader(f, off, size-off), size-off)
	if err != nil {
		return err
	}
	defer s.close()
	if err := s.extract(tmp, nil); err != nil {
		return fmt.Errorf("extract into %s: %w", base, err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, mountDir), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, completeMarker), []byte(sum+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	dir := filepath.Join(base, sum)
	for range 2 {
		err = os.Rename(tmp, dir)
		if err == nil {
			return nil
		}
		if _, serr := os.Stat(filepath.Join(dir, completeMarker)); serr == nil {
			return nil // another run won
		}
		// a directory without the marker is not one this runtime renamed: replace it
		if rerr := os.RemoveAll(dir); rerr != nil {
			return rerr
		}
	}
	return err
}
