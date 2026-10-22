package builder

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// nixBase32 is the alphabet of store path hashes.
const nixBase32 = "0123456789abcdfghijklmnpqrsvwxyz"

const hashLen = 32

// fixup runs the fixup contract (R-NIX-019) on every output: strip (and move debug info),
// shrink RUNPATH, then check references. The fixup's own debug output is never stripped or
// scanned: debug info may name sources.
func (c *Ctx) fixup() error {
	fx := c.Job.Fixup
	if fx.DebugOut {
		if err := c.MkdirAll(c.Outputs["debug"]); err != nil {
			return err
		}
	}
	for _, o := range c.Job.Outputs {
		if c.fixupOutput(o) {
			continue
		}
		root := c.Outputs[o]
		if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
			continue // reported as a missing output afterwards
		}
		if fx.Strip || fx.DebugOut {
			if err := c.stripOutput(o, root); err != nil {
				return err
			}
		}
		if fx.ShrinkRPath {
			if err := c.shrinkRPaths(root); err != nil {
				return err
			}
		}
	}
	return c.checkReferences()
}

// binaryKind classifies a file for stripping.
type binaryKind int

const (
	notBinary binaryKind = iota
	executable
	sharedLib
	object // relocatable object or static library: debug info only
)

func classify(p string) binaryKind {
	f, err := os.Open(p)
	if err != nil {
		return notBinary
	}
	defer f.Close()
	var magic [8]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return notBinary
	}
	if string(magic[:]) == "!<arch>\n" {
		return object
	}
	if string(magic[:4]) != elf.ELFMAG {
		return notBinary
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return notBinary
	}
	defer ef.Close()
	switch ef.Type {
	case elf.ET_EXEC:
		return executable
	case elf.ET_REL:
		return object
	case elf.ET_DYN:
		base := filepath.Base(p)
		if strings.HasSuffix(base, ".so") || strings.Contains(base, ".so.") {
			return sharedLib
		}
		return executable // position-independent executable
	}
	return notBinary
}

// stripOutput strips ELF files and static libraries under bin/, libexec/ and lib/ of one output
// (executables --strip-all, libraries --strip-debug; profile debug keeps everything), moving
// debug info to the debug output first when it is declared.
func (c *Ctx) stripOutput(name, root string) error {
	fx := c.Job.Fixup
	for _, sub := range []string{"bin", "libexec", "lib"} {
		dir := filepath.Join(root, sub)
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
			continue
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			k := classify(p)
			if k == notBinary {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			return withWritable(p, func() error {
				if fx.DebugOut && k != object {
					dbg := filepath.Join(c.Outputs["debug"], "lib", "debug", name, rel+".debug")
					if err := c.MkdirAll(filepath.Dir(dbg)); err != nil {
						return err
					}
					if err := c.tool(fx.Tools.Objcopy, "--only-keep-debug", p, dbg); err != nil {
						return err
					}
					if err := c.strip(p, k, fx.Strip); err != nil {
						return err
					}
					return c.tool(fx.Tools.Objcopy, "--add-gnu-debuglink="+dbg, p)
				}
				if !fx.Strip {
					return nil
				}
				return c.strip(p, k, true)
			})
		})
		if err != nil {
			return fmt.Errorf("fixup strip %s: %w", sub, err)
		}
	}
	return nil
}

// strip removes debug info, and with all also the symbols of executables.
func (c *Ctx) strip(p string, k binaryKind, all bool) error {
	flag := "--strip-debug"
	if all && k == executable {
		flag = "--strip-all"
	}
	return c.tool(c.Job.Fixup.Tools.Strip, flag, p)
}

// withWritable makes p owner-writable for fn and restores its mode.
func withWritable(p string, fn func() error) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	mode := fi.Mode().Perm()
	if mode&0o200 == 0 {
		if err := os.Chmod(p, mode|0o200); err != nil {
			return err
		}
	}
	ferr := fn()
	if err := os.Chmod(p, mode); err != nil && ferr == nil {
		ferr = err
	}
	return ferr
}

// tool runs a fixup tool (strip, objcopy, patchelf) with a minimal environment.
func (c *Ctx) tool(prog string, args ...string) error {
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := &exec.Cmd{Path: prog, Args: append([]string{prog}, args...), Env: []string{"TMPDIR=" + tmp, "LC_ALL=C"},
		Dir: c.BuildTop, Stdout: &out, Stderr: &out}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(prog), strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

// shrinkRPaths drops RUNPATH/RPATH entries outside the store (keeping $ORIGIN-relative ones)
// and lets patchelf remove the entries no needed library comes from.
func (c *Ctx) shrinkRPaths(root string) error {
	patchelf := c.Job.Fixup.Tools.Patchelf
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		entries, isRPath, ok := readRPath(p)
		if !ok {
			return nil
		}
		kept := slices.DeleteFunc(slices.Clone(entries), func(e string) bool {
			return !strings.HasPrefix(e, c.StoreDir+"/") && !strings.HasPrefix(e, "$ORIGIN") && !strings.HasPrefix(e, "${ORIGIN}")
		})
		return withWritable(p, func() error {
			if len(kept) != len(entries) {
				args := []string{"--set-rpath", strings.Join(kept, ":"), p}
				if isRPath {
					args = append([]string{"--force-rpath"}, args...)
				}
				if err := c.tool(patchelf, args...); err != nil {
					return fmt.Errorf("fixup rpath: %w", err)
				}
			}
			if len(kept) == 0 {
				return nil
			}
			if err := c.tool(patchelf, "--shrink-rpath", p); err != nil {
				return fmt.Errorf("fixup rpath: %w", err)
			}
			return nil
		})
	})
}

// readRPath returns the RUNPATH (or else RPATH) entries of a dynamic ELF file; ok is false for
// other files and files without one.
func readRPath(p string) (entries []string, isRPath, ok bool) {
	f, err := elf.Open(p)
	if err != nil {
		return nil, false, false
	}
	defer f.Close()
	if rp, err := f.DynString(elf.DT_RUNPATH); err == nil && len(rp) > 0 {
		return splitPath(rp[0]), false, true
	}
	if rp, err := f.DynString(elf.DT_RPATH); err == nil && len(rp) > 0 {
		return splitPath(rp[0]), true, true
	}
	return nil, false, false
}

func splitPath(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ":") {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// hashPart returns the 32-character hash of a store path.
func (c *Ctx) hashPart(p string) (string, error) {
	base, ok := strings.CutPrefix(p, c.StoreDir+"/")
	if !ok {
		return "", Invalidf("forbidden reference %q is not a store path", p)
	}
	if len(base) < hashLen || strings.Trim(base[:hashLen], nixBase32) != "" {
		return "", Invalidf("forbidden reference %q has no store hash", p)
	}
	return base[:hashLen], nil
}

// checkReferences scans every output except the debug output for the hash of a forbidden store
// path (Nix's algorithm: any 32-character window of the hash alphabet that equals a candidate
// hash, in file contents, names and symlink targets). A hit exits 3 (BOB-E-9003).
func (c *Ctx) checkReferences() error {
	if len(c.Job.Fixup.Forbidden) == 0 {
		return nil
	}
	c.Phase("check-references")
	forbidden := map[string]string{}
	for _, p := range c.Job.Fixup.Forbidden {
		h, err := c.hashPart(p)
		if err != nil {
			return err
		}
		forbidden[h] = p
	}
	for _, o := range c.Job.Outputs {
		if c.fixupOutput(o) {
			continue
		}
		root := c.Outputs[o]
		if _, err := os.Lstat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			s := &scanner{want: forbidden}
			s.scan([]byte(d.Name()))
			switch t := d.Type(); {
			case t&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				s.reset()
				s.scan([]byte(target))
			case t.IsRegular():
				if err := s.file(p); err != nil {
					return err
				}
			}
			if s.found != "" {
				rel, _ := filepath.Rel(root, p)
				return &exitError{code: ExitFixup, msg: fmt.Sprintf("refers to %s, which it must not reference (in output %s, %s)",
					s.found, o, filepath.ToSlash(rel))}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// scanner finds 32-character hashes of the want set in a byte stream.
type scanner struct {
	want  map[string]string
	tail  []byte // the last hashLen-1 bytes of the previous chunk
	found string
}

func (s *scanner) reset() { s.tail = s.tail[:0] }

func (s *scanner) file(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	for s.found == "" {
		n, err := f.Read(buf)
		if n > 0 {
			s.scan(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// scan looks at chunk continuing the stream after tail.
func (s *scanner) scan(chunk []byte) {
	data := append(append([]byte{}, s.tail...), chunk...)
	for i := 0; i+hashLen <= len(data) && s.found == ""; {
		// Check from the window's end backwards; a byte outside the alphabet lets the window
		// jump past it.
		j := hashLen - 1
		for ; j >= 0 && isBase32(data[i+j]); j-- {
		}
		if j >= 0 {
			i += j + 1
			continue
		}
		if p, ok := s.want[string(data[i:i+hashLen])]; ok {
			s.found = p
		}
		i++
	}
	keep := min(len(data), hashLen-1)
	s.tail = append(s.tail[:0], data[len(data)-keep:]...)
}

func isBase32(b byte) bool { return strings.IndexByte(nixBase32, b) >= 0 }
