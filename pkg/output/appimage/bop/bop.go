// Package bop implements bob-builder's kind "appimage" (SPEC R-OUT-003, R-OUT-007, R-OUT-008,
// R-OUT-015 – R-OUT-020): it copies the runtime closure of the bundle's programs into an AppDir
// (minus the Prune globs), writes launch.json, licences.json, the .desktop entry and the icon,
// runs mksquashfs and appends the image to bob's AppImage runtime.
package bop

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
	"github.com/megakuul/bob/pkg/builder/bspec"
)

// Kinds returns the bundle kind; cmd/bob-builder merges it into its kind table. The output is
// byte-exact: no fixup.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{"appimage": {Run: run, NoFixup: true}}
}

// codePrune is BOB-E-2015 (a prune pattern removes a library the bundle needs).
const codePrune = 2015

func run(c *builder.Ctx, raw json.RawMessage) error {
	var a Args
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return builder.Invalidf("appimage args: %v", err)
	}
	if err := check(a); err != nil {
		return err
	}
	b := &bundle{c: c, a: a, store: c.StoreDir, pruned: map[string]string{}}
	return b.build()
}

func check(a Args) error {
	switch {
	case a.Name == "" || strings.ContainsAny(a.Name, "/\x00") || a.Name[0] == '.':
		return builder.Invalidf("bad bundle name %q", a.Name)
	case a.Arch != "x86_64" && a.Arch != "aarch64":
		return builder.Invalidf("bad arch %q", a.Arch)
	case a.Mode != "auto" && a.Mode != "userns":
		return builder.Invalidf("bad mode %q", a.Mode)
	case a.Runtime == "" || a.Mksquashfs == "" || a.Entry == "" || a.Closure == "":
		return builder.Invalidf("runtime, mksquashfs, entry and closure are required")
	case len(a.Locales) > 0 && (a.Localedef == "" || a.I18n == ""):
		return builder.Invalidf("locales need localedef and i18n")
	}
	for _, p := range a.Prune {
		if _, err := globRE(p); err != nil {
			return builder.Invalidf("prune pattern %q: %v", p, err)
		}
	}
	return nil
}

type bundle struct {
	c       *builder.Ctx
	a       Args
	store   string            // /nix/store
	appDir  string            // <tmp>/AppDir
	closure []string          // store paths, sorted
	pruned  map[string]string // removed /nix/store/… file → the pattern that removed it
	extra   []string          // store-like paths the builder adds (locale archive)
}

// bundleStore is the AppDir's store directory for path p ("/nix/store/x/y" → <appDir>/nix/store/x/y).
func (b *bundle) inBundle(p string) string {
	return filepath.Join(b.appDir, filepath.FromSlash(strings.TrimPrefix(p, "/")))
}

func (b *bundle) build() error {
	c, a := b.c, b.a
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	b.appDir = filepath.Join(tmp, "AppDir")
	infos, err := c.ReferencesGraph(a.Closure)
	if err != nil {
		return err
	}
	for _, in := range infos {
		b.closure = append(b.closure, in.Path)
	}
	c.Phase("copy")
	prune := slices.Clone(a.Prune)
	if a.NoIconv {
		prune = append(prune, b.store+"/*-glibc-*/lib/gconv/**")
	}
	if err := b.copyClosure(prune); err != nil {
		return err
	}
	if err := b.checkNeeded(); err != nil {
		return err
	}
	c.Phase("appdir")
	b.warnFidelity()
	if len(a.Locales) > 0 {
		if err := b.localeArchive(tmp); err != nil {
			return err
		}
	}
	if err := b.writeMeta(); err != nil {
		return err
	}
	c.Phase("squashfs")
	img := filepath.Join(tmp, "image.squashfs")
	// SPEC's -all-time 1 -mkfs-time 1: mksquashfs refuses them next to SOURCE_DATE_EPOCH,
	// which the builder environment always sets to 1 (R-NIX-016) and which has the same effect.
	if err := c.Exec(bspec.ExecArgs{Argv: []string{a.Mksquashfs, b.appDir, img,
		"-all-root", "-no-xattrs",
		"-comp", "zstd", "-Xcompression-level", "19", "-b", "1M",
		"-noappend", "-quiet", "-no-progress"}}); err != nil {
		return fmt.Errorf("mksquashfs: %w", err)
	}
	return b.writeAppImage(img)
}

// ---- closure copy and pruning (R-OUT-007) ----

// globRE compiles a prune glob: "*" and "?" do not cross "/", "**" does.
func globRE(glob string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(glob, "/nix/store/") {
		return nil, errors.New("must start with /nix/store/")
	}
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch ch := glob[i]; ch {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				re.WriteString(".*")
				i++
			} else {
				re.WriteString("[^/]*")
			}
		case '?':
			re.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 2 {
				return nil, errors.New("unterminated [")
			}
			class := glob[i+1 : i+j]
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			re.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += j
		default:
			re.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	re.WriteString("$")
	return regexp.Compile(re.String())
}

func (b *bundle) copyClosure(prune []string) error {
	type pat struct {
		glob string
		re   *regexp.Regexp
	}
	var pats []pat
	for _, g := range prune {
		re, err := globRE(g)
		if err != nil {
			return builder.Invalidf("prune pattern %q: %v", g, err)
		}
		pats = append(pats, pat{g, re})
	}
	match := func(p string) string {
		for _, x := range pats {
			if x.re.MatchString(p) {
				return x.glob
			}
		}
		return ""
	}
	if err := os.MkdirAll(b.inBundle(b.store), 0o755); err != nil {
		return err
	}
	for _, root := range b.closure {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			dst := b.inBundle(p)
			if p != root {
				if g := match(p); g != "" {
					b.pruned[p] = g
					if d.IsDir() {
						return b.recordPruned(p, g)
					}
					return nil
				}
			}
			switch t := d.Type(); {
			case t.IsDir():
				return os.Mkdir(dst, 0o755)
			case t&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				return os.Symlink(target, dst)
			case t.IsRegular():
				return copyFile(p, dst)
			}
			return fmt.Errorf("%s: special file", p)
		})
		if err != nil {
			return fmt.Errorf("copy %s: %w", root, err)
		}
	}
	return nil
}

// recordPruned records every file below a pruned directory and skips the directory.
func (b *bundle) recordPruned(dir, glob string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b.pruned[p] = glob
		}
		return nil
	})
	return fs.SkipDir
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, fi.Size())); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// elfInfo is what the bundle needs of an ELF file.
type elfInfo struct {
	needed  []string
	runpath []string
	interp  string
}

// readELF returns nil for files that are not dynamic ELF objects.
func readELF(file string) *elfInfo {
	f, err := elf.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	in := &elfInfo{}
	in.needed, _ = f.ImportedLibraries()
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		if v, err := f.DynString(tag); err == nil {
			for _, s := range v {
				in.runpath = append(in.runpath, strings.Split(s, ":")...)
			}
		}
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			b := make([]byte, p.Filesz)
			if _, err := p.ReadAt(b, 0); err == nil {
				in.interp = strings.TrimRight(string(b), "\x00")
			}
		}
	}
	return in
}

// prunedNeed is a library a kept ELF file needs that a prune pattern removed.
type prunedNeed struct{ pattern, file, user string }

// checkNeeded fails when a pruned file is a library a kept ELF file of the bundle needs
// (DT_NEEDED resolved in its RUNPATH): BOB-E-2015. The entries are checked first, so the
// message names the program when it is the one that needs the library.
func (b *bundle) checkNeeded() error {
	if len(b.pruned) == 0 {
		return nil
	}
	var found *prunedNeed
	for _, e := range b.entryNames() {
		if real, err := b.resolve(e[1]); err == nil {
			if found = b.prunedNeedFrom(real, true); found != nil {
				break
			}
		}
	}
	if found == nil {
		err := filepath.WalkDir(b.inBundle(b.store), func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || found != nil {
				return err
			}
			found = b.prunedNeedFrom("/"+filepath.ToSlash(strings.TrimPrefix(p, b.appDir+"/")), false)
			return nil
		})
		if err != nil {
			return err
		}
	}
	if found == nil {
		return nil
	}
	msg := fmt.Sprintf("%s: prune pattern %q removes %s, which %s needs", b.c.Job.Label, found.pattern, found.file, found.user)
	b.c.CodedDiag("error", codePrune, map[string]string{"label": b.c.Job.Label, "pattern": found.pattern, "file": found.file, "user": found.user}, msg)
	return errors.New(msg)
}

// prunedNeedFrom checks the store file root of the AppDir (nil: fine or not a dynamic ELF file).
// With deep set it also follows the kept libraries root loads (each resolved in its own
// RUNPATH, as ld.so does) and names root as the one that needs a pruned library: for a bundle
// entry the message names the program (SPEC R-OUT-007: "removes libssl.so.3, which api needs"
// although api loads libssl through libfoo.so).
func (b *bundle) prunedNeedFrom(root string, deep bool) *prunedNeed {
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if seen[f] {
			continue
		}
		seen[f] = true
		in := readELF(b.inBundle(f))
		if in == nil {
			continue
		}
		for _, lib := range in.needed {
			var prunedBy string
			kept := false
			for _, dir := range in.runpath {
				dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", path.Dir(f)), "$ORIGIN", path.Dir(f))
				cand := path.Join(dir, lib)
				if !strings.HasPrefix(cand, b.store+"/") {
					continue
				}
				if real, err := b.resolve(cand); err == nil {
					kept = true
					if deep {
						queue = append(queue, real)
					}
					break
				}
				if g, ok := b.pruned[cand]; ok && prunedBy == "" {
					prunedBy = g
				}
			}
			if !kept && prunedBy != "" {
				return &prunedNeed{prunedBy, lib, path.Base(root)}
			}
		}
	}
	return nil
}

// ---- metadata ----

// entryNames returns the bundle's entries: the main one under the bundle name, then the others.
func (b *bundle) entryNames() [][2]string {
	out := [][2]string{{b.a.Name, b.a.Entry}}
	names := make([]string, 0, len(b.a.Entries))
	for n := range b.a.Entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, [2]string{n, b.a.Entries[n]})
	}
	return out
}

// resolve follows symlinks of a store path inside the AppDir (absolute store targets mapped).
func (b *bundle) resolve(p string) (string, error) {
	cur := p
	for range 40 {
		fi, err := os.Lstat(b.inBundle(cur))
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return cur, nil
		}
		t, err := os.Readlink(b.inBundle(cur))
		if err != nil {
			return "", err
		}
		if path.IsAbs(t) {
			cur = path.Clean(t)
		} else {
			cur = path.Join(path.Dir(cur), t)
		}
		if !strings.HasPrefix(cur, b.store+"/") {
			return "", fmt.Errorf("%s links outside the store", p)
		}
	}
	return "", fmt.Errorf("%s: too many symbolic links", p)
}

// warnFidelity warns about entries that cannot run in ldso mode (R-OUT-019).
func (b *bundle) warnFidelity() {
	if b.a.Mode != "auto" {
		return
	}
	const consequence = "; it cannot run on hosts without user namespaces. Set Mode: appimage.UsernsOnly to make this explicit."
	warn := func(what string) {
		reason := what + consequence
		b.c.CodedDiag("warning", codeWarning, map[string]string{"label": b.c.Job.Label, "reason": reason}, b.c.Job.Label+": "+reason)
	}
	entryFiles := map[string]bool{}
	for _, e := range b.entryNames() {
		real, err := b.resolve(e[1])
		if err != nil {
			continue
		}
		entryFiles[real] = true
		switch wrapperKind(b.inBundle(real)) {
		case "script":
			warn(fmt.Sprintf("entry %q is a script", e[0]))
		case "wrapper":
			warn(fmt.Sprintf("entry %q is a wrapper that runs another store path", e[0]))
		}
	}
	// makeWrapper-style wrappers elsewhere in the closure (programs in bin/ of a store path).
	var wrappers []string
	for _, p := range b.closure {
		ents, err := os.ReadDir(b.inBundle(p + "/bin"))
		if err != nil {
			continue
		}
		for _, e := range ents {
			f := p + "/bin/" + e.Name()
			if !e.Type().IsRegular() || entryFiles[f] {
				continue
			}
			if k := wrapperKind(b.inBundle(f)); k == "wrapper" || k == "script" && execsStore(b.inBundle(f)) {
				wrappers = append(wrappers, f)
			}
		}
	}
	if len(wrappers) > 0 {
		what := fmt.Sprintf("the closure contains a wrapper that runs another store path (%s", wrappers[0])
		if len(wrappers) > 1 {
			what += fmt.Sprintf(" and %d more", len(wrappers)-1)
		}
		warn(what + ")")
	}
}

// wrapperKind classifies a program: "script" (shebang), "wrapper" (nixpkgs makeBinaryWrapper
// ELF, which embeds its makeCWrapper call), "" otherwise.
func wrapperKind(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return ""
	}
	switch {
	case string(head[:2]) == "#!":
		return "script"
	case string(head) == "\x7fELF":
		data, err := io.ReadAll(io.LimitReader(f, 16<<20))
		if err == nil && bytes.Contains(data, []byte("makeCWrapper")) {
			return "wrapper"
		}
	}
	return ""
}

// execsStore reports whether a shell script execs a /nix/store program (makeWrapper's
// `exec -a "$0" "/nix/store/…" "$@"`).
func execsStore(file string) bool {
	data, err := os.ReadFile(file)
	if err != nil || len(data) > 1<<20 {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "exec ") && strings.Contains(l, "/nix/store/") {
			return true
		}
	}
	return false
}

// codeWarning is BOB-E-2016, a warning of a successful build ("{label}: {reason}").
const codeWarning = 2016

// localeArchive builds a locale archive of the requested locales (R-OUT-017) into a
// store-like directory of the AppDir.
func (b *bundle) localeArchive(tmp string) error {
	src := filepath.Join(tmp, "locales")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	var dirs []string
	for _, l := range b.a.Locales {
		lang, charset, ok := strings.Cut(l, ".")
		mod := ""
		if i := strings.IndexByte(charset, '@'); i >= 0 {
			charset, mod = charset[:i], charset[i:]
		}
		if !ok || lang == "" || charset == "" {
			return builder.Invalidf("locale %q is not <language>.<charset>", l)
		}
		out := filepath.Join(src, l)
		charmap, err := b.charmap(tmp, charset)
		if err != nil {
			return err
		}
		if err := b.c.Exec(bspec.ExecArgs{Argv: []string{b.a.Localedef, "--no-archive", "-c", "-i", lang + mod, "-f", charmap, out},
			Env: map[string]string{"I18NPATH": b.a.I18n}}); err != nil {
			// localedef exits 1 for warnings but writes the locale
			if _, serr := os.Stat(filepath.Join(out, "LC_CTYPE")); serr != nil {
				return fmt.Errorf("localedef %s: %w", l, err)
			}
		}
		dirs = append(dirs, out)
	}
	prefix := filepath.Join(tmp, "locale-prefix")
	// localedef --prefix writes <prefix>/<glibc>/lib/locale/locale-archive; find it afterwards.
	args := append([]string{b.a.Localedef, "--prefix", prefix, "--add-to-archive", "--replace"}, dirs...)
	if err := b.makeArchiveDirs(prefix); err != nil {
		return err
	}
	if err := b.c.Exec(bspec.ExecArgs{Argv: args}); err != nil {
		return fmt.Errorf("localedef --add-to-archive: %w", err)
	}
	var archive string
	_ = filepath.WalkDir(prefix, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "locale-archive" && d.Type().IsRegular() {
			archive = p
		}
		return nil
	})
	if archive == "" {
		return errors.New("localedef wrote no locale-archive")
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	dir := b.store + "/" + contentName(data) + "-locale-archive"
	if err := os.MkdirAll(b.inBundle(dir), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(b.inBundle(dir+"/locale-archive"), data, 0o644); err != nil {
		return err
	}
	b.extra = append(b.extra, dir)
	return nil
}

// charmap unpacks glibc's gzip-compressed character map of charset (localedef would need a gzip
// program for it) and returns the file.
func (b *bundle) charmap(tmp, charset string) (string, error) {
	dst := filepath.Join(tmp, "charmaps", charset)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	f, err := os.Open(filepath.Join(b.a.I18n, "charmaps", charset+".gz"))
	if err != nil {
		return "", fmt.Errorf("locale charset %s: %w", charset, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("locale charset %s: %w", charset, err)
	}
	data, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return "", fmt.Errorf("locale charset %s: %w", charset, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	return dst, os.WriteFile(dst, data, 0o644)
}

// makeArchiveDirs creates the directory localedef writes its archive to below prefix: the
// lib/locale of the glibc localedef belongs to (its compiled-in path).
func (b *bundle) makeArchiveDirs(prefix string) error {
	data, err := os.ReadFile(b.a.Localedef)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^/\x00]*/lib/locale/locale-archive`)
	for _, m := range re.FindAll(data, -1) {
		if err := os.MkdirAll(filepath.Join(prefix, filepath.Dir(string(m))), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// contentName is a 32-character name derived from data (for builder-made store-like paths).
func contentName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// libDirs are the directories of the AppDir store holding shared libraries (lib*.so*), as
// store paths.
func (b *bundle) libDirs() ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(b.inBundle(b.store), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// libraries only: plug-in directories (gconv, OpenSSL engines) are not searched
		if n := d.Name(); strings.HasPrefix(n, "lib") && (strings.HasSuffix(n, ".so") || strings.Contains(n, ".so.")) {
			seen["/"+filepath.ToSlash(strings.TrimPrefix(filepath.Dir(p), b.appDir+"/"))] = true
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, err
}

// launchFile is .bob/launch.json (SPEC R-OUT-015).
type launchFile struct {
	Schema       string            `json:"schema"`
	Entry        string            `json:"entry"`
	Entries      map[string]string `json:"entries"`
	Interp       string            `json:"interp"`
	LibDirs      []string          `json:"libDirs"`
	Env          []string          `json:"env"`
	Mode         string            `json:"mode"`
	HostGraphics bool              `json:"hostGraphics"`
}

func (b *bundle) writeMeta() error {
	a := b.a
	if err := os.MkdirAll(filepath.Join(b.appDir, ".bob"), 0o755); err != nil {
		return err
	}
	libs, err := b.libDirs()
	if err != nil {
		return err
	}
	l := launchFile{Schema: "bob.appimage.launch/v1", Entry: a.Entry, Entries: map[string]string{},
		LibDirs: libs, Env: b.env(), Mode: a.Mode, HostGraphics: a.HostGraphics}
	for n, e := range a.Entries {
		l.Entries[n] = e
	}
	if real, err := b.resolve(a.Entry); err == nil {
		if in := readELF(b.inBundle(real)); in != nil {
			l.Interp = in.interp
		}
	}
	if l.Interp == "" { // a static main program: take the loader of another entry
		for _, e := range b.entryNames()[1:] {
			if real, err := b.resolve(e[1]); err == nil {
				if in := readELF(b.inBundle(real)); in != nil && in.interp != "" {
					l.Interp = in.interp
					break
				}
			}
		}
	}
	if err := writeJSON(filepath.Join(b.appDir, ".bob", "launch.json"), l); err != nil {
		return err
	}
	if err := builder.WriteLicences(filepath.Join(b.appDir, ".bob", "licences.json"), b.store, b.closure, a.Licences); err != nil {
		return err
	}
	if err := b.desktop(); err != nil {
		return err
	}
	return copyFile(a.Runtime, filepath.Join(b.appDir, "AppRun"))
}

func writeJSON(file string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0o644)
}

// env is launch.json's env: the data paths of R-OUT-017, overridden by the user's Env.
func (b *bundle) env() []string {
	env := map[string]string{}
	const store = "${STORE}/"
	base := func(p string) string { return strings.TrimPrefix(p, b.store+"/") }
	if b.a.CACert != "" {
		env["SSL_CERT_FILE"] = store + base(b.a.CACert) + "/etc/ssl/certs/ca-bundle.crt"
	}
	for _, p := range b.closure {
		name := base(p)[33:]
		switch {
		case !b.a.NoIconv && strings.HasPrefix(name, "glibc-") && !strings.Contains(name[6:], "-bin") && dirExists(b.inBundle(p+"/lib/gconv")):
			env["GCONV_PATH"] = store + base(p) + "/lib/gconv"
		case strings.HasPrefix(name, "tzdata-") && dirExists(b.inBundle(p+"/share/zoneinfo")):
			env["TZDIR"] = store + base(p) + "/share/zoneinfo"
		}
	}
	for _, x := range b.extra {
		if strings.HasSuffix(x, "-locale-archive") {
			env["LOCALE_ARCHIVE"] = store + base(x) + "/locale-archive"
		}
	}
	for k, v := range b.a.Env {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// placeholderIcon is the icon of bundles without a Desktop icon.
const placeholderIcon = `<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256" viewBox="0 0 256 256">
<rect x="16" y="16" width="224" height="224" fill="#2b2b2b"/>
<path d="M64 88l40 40-40 40" fill="none" stroke="#f0f0f0" stroke-width="16"/>
<path d="M120 176h72" stroke="#f0f0f0" stroke-width="16"/>
</svg>
`

func (b *bundle) desktop() error {
	a := b.a
	name := a.Desktop.Name
	if name == "" {
		name = a.Name
	}
	cats := strings.Join(a.Desktop.Categories, ";")
	if cats == "" {
		cats = "Utility"
	}
	text := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=%s\nIcon=%s\nCategories=%s;\nTerminal=%t\n",
		name, a.Name, a.Name, cats, a.Desktop.Terminal)
	if err := os.WriteFile(filepath.Join(b.appDir, a.Name+".desktop"), []byte(text), 0o644); err != nil {
		return err
	}
	ext, data := ".svg", []byte(placeholderIcon)
	if a.Desktop.Icon != "" {
		d, err := os.ReadFile(a.Desktop.Icon)
		if err != nil {
			return err
		}
		data = d
		if bytes.HasPrefix(d, []byte("\x89PNG")) {
			ext = ".png"
		}
	}
	icon := a.Name + ext
	if err := os.WriteFile(filepath.Join(b.appDir, icon), data, 0o644); err != nil {
		return err
	}
	return os.Symlink(icon, filepath.Join(b.appDir, ".DirIcon"))
}

// ---- the AppImage file ----

func (b *bundle) writeAppImage(img string) error {
	rt, err := os.ReadFile(b.a.Runtime)
	if err != nil {
		return err
	}
	end, err := elfEnd(rt)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if end != int64(len(rt)) {
		return fmt.Errorf("runtime: the ELF ends at %d but the file has %d bytes", end, len(rt))
	}
	copy(rt[8:11], "AI\x02") // AppImage type 2 magic in e_ident padding
	out := b.c.Outputs["out"]
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(out, b.a.Name+"-"+b.a.Arch+".AppImage"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := f.Write(rt); err != nil {
		f.Close()
		return err
	}
	in, err := os.Open(img)
	if err != nil {
		f.Close()
		return err
	}
	defer in.Close()
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// elfEnd is where an ELF64 file's last segment or section ends (the runtime finds its image
// there).
func elfEnd(b []byte) (int64, error) {
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var end int64
	if len(b) >= 64 {
		le := binary.LittleEndian
		end = int64(le.Uint64(b[40:])) + int64(le.Uint16(b[58:]))*int64(le.Uint16(b[60:]))
	}
	for _, p := range f.Progs {
		end = max(end, int64(p.Off+p.Filesz))
	}
	for _, s := range f.Sections {
		if s.Type != elf.SHT_NOBITS {
			end = max(end, int64(s.Offset+s.FileSize))
		}
	}
	return end, nil
}
