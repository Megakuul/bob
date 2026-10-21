// Package appimage builds a type-2 AppImage from a store closure. Spike code.
//
// Layout of the squashfs (the AppDir):
//
//	AppRun               static launcher (cmd/bobrun) for the target arch
//	<name>.desktop       minimal desktop entry (Terminal=true)
//	.DirIcon, <name>.svg placeholder icon
//	.bob/launch.json     entry, interpreter, library dirs, env
//	nix/store/...        the closure, unchanged
package appimage

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bobspike/s08/closure"
)

type Bundle struct {
	Name     string            `json:"name"`
	Entry    string            `json:"entry"`    // /nix/store/…/bin/prog
	Entries  map[string]string `json:"entries"`  // extra programs, selectable with BOB_ENTRY
	Roots    []string          `json:"roots"`    // extra store paths to include
	Env      []string          `json:"env"`      // KEY=VALUE; ${STORE} = bundled store dir at run time
	CACerts  string            `json:"cacerts"`  // store path of a CA bundle package
	Locales  string            `json:"locales"`  // store path containing lib/locale/locale-archive (optional)
	Runtime  string            `json:"runtime"`  // type-2 runtime ELF
	Launcher string            `json:"launcher"` // AppRun binary for the target arch
	Mksquash string            `json:"mksquashfs"`
	Comp     string            `json:"comp"` // zstd (default), gzip, xz
	Work     string            `json:"work"`
	Prune    []string          `json:"prune"` // globs on /nix/store/… paths removed from the AppDir
	Strip    bool              `json:"strip"` // strip entry binaries (spike stand-in for a package fixup)
	Out      string            `json:"out"`
}

// Launch is .bob/launch.json, read by AppRun.
type Launch struct {
	Entry   string            `json:"entry"`
	Entries map[string]string `json:"entries,omitempty"`
	Interp  string            `json:"interp"`  // PT_INTERP of the entry
	LibDirs []string          `json:"libDirs"` // for the ld.so fallback, in search order
	Env     []string          `json:"env"`
}

type Stats struct {
	SHA256       string
	Size         int64
	ClosureBytes int64
	Paths        int
}

func Build(ctx context.Context, b Bundle) (Stats, error) {
	var st Stats
	roots := append([]string{storePath(b.Entry)}, b.Roots...)
	for _, e := range b.Entries {
		roots = append(roots, storePath(e))
	}
	env := append([]string{}, b.Env...)
	if b.CACerts != "" {
		roots = append(roots, b.CACerts)
		env = append(env, "SSL_CERT_FILE=${STORE}"+strings.TrimPrefix(b.CACerts, "/nix/store")+"/etc/ssl/certs/ca-bundle.crt")
	}
	if b.Locales != "" {
		roots = append(roots, b.Locales)
		la := "${STORE}" + strings.TrimPrefix(b.Locales, "/nix/store") + "/lib/locale/locale-archive"
		env = append(env, "LOCALE_ARCHIVE="+la)
	}
	c, err := closure.Query(ctx, roots...)
	if err != nil {
		return st, err
	}
	st.ClosureBytes, st.Paths = c.Size(), len(c.Paths)
	// glibc finds iconv modules through a compiled-in store path; the ld.so mode needs it.
	for _, p := range c.Sorted() {
		if strings.Contains(filepath.Base(p), "-glibc-") {
			if _, err := os.Stat(p + "/lib/gconv"); err == nil {
				env = append(env, "GCONV_PATH=${STORE}"+strings.TrimPrefix(p, "/nix/store")+"/lib/gconv")
			}
		}
	}
	appdir := filepath.Join(b.Work, b.Name+".AppDir")
	if err := os.RemoveAll(appdir); err != nil {
		return st, err
	}
	if err := os.MkdirAll(filepath.Join(appdir, "nix", "store"), 0o755); err != nil {
		return st, err
	}
	for _, p := range c.Sorted() {
		if out, err := exec.CommandContext(ctx, "cp", "-a", p, filepath.Join(appdir, "nix", "store")+"/").CombinedOutput(); err != nil {
			return st, fmt.Errorf("copy %s: %v: %s", p, err, out)
		}
	}
	if err := prune(appdir, b.Prune); err != nil {
		return st, err
	}
	if b.Strip {
		for _, e := range append([]string{b.Entry}, mapValues(b.Entries)...) {
			p := filepath.Join(appdir, e)
			_ = os.Chmod(filepath.Dir(p), 0o755)
			_ = os.Chmod(p, 0o755)
			if out, err := exec.CommandContext(ctx, "strip", p).CombinedOutput(); err != nil {
				return st, fmt.Errorf("strip %s: %v: %s", e, err, out)
			}
		}
	}
	interp, err := elfInterp(b.Entry)
	if err != nil {
		return st, err
	}
	l := Launch{Entry: b.Entry, Entries: b.Entries, Interp: interp, LibDirs: libDirs(b.Entry, c), Env: env}
	lj, _ := json.MarshalIndent(l, "", " ")
	files := map[string][]byte{
		".bob/launch.json":  lj,
		b.Name + ".desktop": []byte("[Desktop Entry]\nType=Application\nName=" + b.Name + "\nExec=AppRun\nIcon=" + b.Name + "\nTerminal=true\nCategories=Utility;\n"),
		b.Name + ".svg":     []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16"/></svg>`),
	}
	for n, d := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(appdir, n)), 0o755); err != nil {
			return st, err
		}
		if err := os.WriteFile(filepath.Join(appdir, n), d, 0o644); err != nil {
			return st, err
		}
	}
	if err := os.Symlink(b.Name+".svg", filepath.Join(appdir, ".DirIcon")); err != nil {
		return st, err
	}
	if err := copyFile(b.Launcher, filepath.Join(appdir, "AppRun"), 0o755); err != nil {
		return st, err
	}
	comp := b.Comp
	if comp == "" {
		comp = "zstd"
	}
	sqfs := filepath.Join(b.Work, b.Name+".squashfs")
	os.Remove(sqfs)
	// Reproducible: fixed times, root-owned, no xattrs, sorted (mksquashfs sorts by name).
	args := []string{appdir, sqfs, "-noappend", "-quiet", "-no-progress", "-comp", comp, "-all-root",
		"-no-xattrs", "-all-time", "1", "-mkfs-time", "1", "-b", "1M", "-processors", "4"}
	if comp == "zstd" {
		args = append(args, "-Xcompression-level", "19")
	}
	if out, err := exec.CommandContext(ctx, b.Mksquash, args...).CombinedOutput(); err != nil {
		return st, fmt.Errorf("mksquashfs: %v: %s", err, out)
	}
	f, err := os.OpenFile(b.Out+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return st, err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h)
	for _, p := range []string{b.Runtime, sqfs} {
		r, err := os.Open(p)
		if err != nil {
			f.Close()
			return st, err
		}
		n, err := io.Copy(w, r)
		r.Close()
		if err != nil {
			f.Close()
			return st, err
		}
		st.Size += n
	}
	if err := f.Close(); err != nil {
		return st, err
	}
	st.SHA256 = hex.EncodeToString(h.Sum(nil))
	return st, os.Rename(b.Out+".tmp", b.Out)
}

func storePath(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, "/nix/store/"), "/", 2)
	return "/nix/store/" + parts[0]
}

func elfInterp(p string) (string, error) {
	f, err := elf.Open(p)
	if err != nil {
		return "", fmt.Errorf("entry %s: %w", p, err)
	}
	defer f.Close()
	for _, pr := range f.Progs {
		if pr.Type == elf.PT_INTERP {
			b, err := io.ReadAll(pr.Open())
			if err != nil {
				return "", err
			}
			return strings.TrimRight(string(b), "\x00"), nil
		}
	}
	return "", nil // static binary
}

// libDirs: the entry's RUNPATH first, then every lib/ dir of the closure (sorted).
func libDirs(entry string, c *closure.Closure) []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if f, err := elf.Open(entry); err == nil {
		for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
			if v, err := f.DynString(tag); err == nil {
				for _, s := range v {
					for _, d := range strings.Split(s, ":") {
						if d != "" {
							add(d)
						}
					}
				}
			}
		}
		f.Close()
	}
	for _, p := range c.Sorted() {
		if fi, err := os.Stat(p + "/lib"); err == nil && fi.IsDir() {
			add(p + "/lib")
		}
	}
	return out
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// prune removes AppDir entries whose store path matches one of the globs.
func prune(appdir string, globs []string) error {
	if len(globs) == 0 {
		return nil
	}
	var hits []string
	err := filepath.Walk(appdir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := "/" + strings.TrimPrefix(p, appdir+"/")
		for _, g := range globs {
			if ok, _ := filepath.Match(g, rel); ok {
				hits = append(hits, p)
				if fi.IsDir() {
					return filepath.SkipDir
				}
			}
		}
		if fi.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, h := range hits {
		_ = filepath.Walk(h, func(p string, fi os.FileInfo, err error) error {
			if err == nil && fi.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
		if err := os.RemoveAll(h); err != nil {
			return err
		}
	}
	return nil
}
