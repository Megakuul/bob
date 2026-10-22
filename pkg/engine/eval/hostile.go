package eval

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// The repository scan, the dependency loading and the generation run on the host, before and
// outside the evaluator sandbox (R-FW-045), on repositories a worker does not trust
// (AGENTS.md §5.4). Everything they read is size-capped and every module path, version and
// directory taken from a go.mod is validated before it names a file (review findings F2, F8).

// Caps of the host-side reads.
const (
	maxBobGoBytes = 1 << 20  // one bob.go
	maxGoModBytes = 1 << 20  // one go.mod
	maxGoSumBytes = 16 << 20 // one go.sum
	maxBobFiles   = 10_000   // bob.go files of one repository (dependency repositories each)
	maxScanDepth  = 128      // directory nesting below a repository root
)

// capOf is the size cap of a scanned file by its name.
func capOf(name string) int64 {
	switch path.Base(name) {
	case "go.sum":
		return maxGoSumBytes
	case "go.mod":
		return maxGoModBytes
	}
	return maxBobGoBytes
}

// readCapped reads the regular file p (never through a final symlink) up to max bytes; a
// larger file is a definition error naming shown.
func readCapped(p, shown string, max int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ir.Errors{ir.DefinitionError(shown, 0, 0, "", "is a symlink; bob reads only regular files here")}
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ir.Errors{ir.DefinitionError(shown, 0, 0, "", "is not a regular file")}
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, tooLarge(shown, max)
	}
	return b, nil
}

func tooLarge(shown string, max int64) error {
	return ir.Errors{ir.DefinitionError(shown, 0, 0, "", fmt.Sprintf("is larger than the limit of %d bytes", max))}
}

var (
	// modElemRe is the character set of a module path element (cmd/go's module.CheckPath).
	modElemRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._~-]+$`) })
	// modVersionRe is a Go module version: canonical semver with optional pre-release and build
	// metadata (pseudo-versions and +incompatible included).
	modVersionRe = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	})
	// goLineRe is the version of a go directive ("1.26", "1.26.0", "1.27rc1").
	goLineRe = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^[1-9][0-9]*(\.(0|[1-9][0-9]*)){1,2}((rc|beta)[1-9][0-9]*)?$`)
	})
)

// checkModulePath validates a module path from a go.mod: the rules of cmd/go's
// module.CheckPath (simplified), so it can name a directory below .bob/eval/mods and a line
// of the generated go.mod. A dependency's path needs a dotted, lower-case first element; the
// main module's path (main) only safe elements.
func checkModulePath(p string, main bool) error {
	if p == "" || len(p) > 512 {
		return fmt.Errorf("module path %q has an invalid length", clip(p))
	}
	elems := strings.Split(p, "/")
	if !main && (!strings.Contains(elems[0], ".") || strings.ToLower(elems[0]) != elems[0] || strings.HasPrefix(elems[0], "-")) {
		return fmt.Errorf("module path %q does not start with a domain name", clip(p))
	}
	for _, e := range elems {
		if e == "" || !modElemRe().MatchString(e) || strings.HasPrefix(e, ".") || strings.HasSuffix(e, ".") {
			return fmt.Errorf("module path %q has an invalid element %q", clip(p), clip(e))
		}
	}
	return nil
}

// checkVersion validates a required module version.
func checkVersion(mp, v string) error {
	if !modVersionRe().MatchString(v) || len(v) > 256 {
		return fmt.Errorf("module %s: version %q is not a valid module version", mp, clip(v))
	}
	return nil
}

// checkGoLine validates the version of a go directive.
func checkGoLine(v string) error {
	if v != "" && !goLineRe().MatchString(v) {
		return fmt.Errorf("go version %q is not valid", clip(v))
	}
	return nil
}

// replaceDir resolves a directory replace target of the root go.mod. In an untrusted
// repository (a worker) the target, with symlinks resolved, must stay inside the repository;
// locally it may name any directory of the developer's machine (sibling checkouts).
func replaceDir(root, mp, target string, untrusted bool) (string, error) {
	src := target
	if !filepath.IsAbs(src) {
		src = filepath.Join(root, src)
	}
	if !untrusted {
		return src, nil
	}
	refuse := ir.Errors{ir.DefinitionError("go.mod", 0, 0, "",
		fmt.Sprintf("replace %s => %s: the directory must be inside the repository", mp, clip(target)))}
	if filepath.IsAbs(target) || !within(filepath.Clean(root), filepath.Clean(src)) {
		return "", refuse
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("replace %s => %s: %w", mp, clip(target), err)
		}
		return "", err
	}
	if !within(realRoot, realSrc) {
		return "", refuse
	}
	return realSrc, nil
}

// within reports whether p is dir or below it (both clean, absolute).
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// genName validates a name below .bob/eval before generate writes it.
func genName(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") ||
		strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("refusing to write %q outside %s", clip(name), genDir)
	}
	return nil
}

// clip shortens hostile strings for error messages.
func clip(s string) string {
	if len(s) > 80 {
		return strings.ToValidUTF8(s[:80], "") + "…"
	}
	return strings.ToValidUTF8(s, "?")
}

// walkBobRepo lists the bob.go files of the module at src (module-relative directories, "."
// for its root) with the scan's rules: skipped directory names, nested modules, regular files
// only, at most maxBobFiles files and maxScanDepth levels.
func walkBobRepo(src, shown string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == src {
				return nil
			}
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			if strings.Count(rel, "/")+1 > maxScanDepth {
				return tooDeep(shown)
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "bob.go" || !d.Type().IsRegular() {
			return nil
		}
		if len(dirs) >= maxBobFiles {
			return tooMany(shown)
		}
		dirs = append(dirs, path.Dir(rel))
		return nil
	})
	return dirs, err
}

func tooDeep(shown string) error {
	return ir.Errors{ir.DefinitionError(shown, 0, 0, "", fmt.Sprintf("has directories nested deeper than %d levels", maxScanDepth))}
}

func tooMany(shown string) error {
	return ir.Errors{ir.DefinitionError(shown, 0, 0, "", fmt.Sprintf("has more than %d bob.go files", maxBobFiles))}
}

// checkGenDir refuses a repository whose .bob or .bob/eval is not a plain directory: bob writes
// the generated evaluator sources there, and a committed symlink would redirect those writes
// outside the repository.
func checkGenDir(root string) error {
	for _, d := range []string{path.Dir(genDir), genDir} {
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(d)))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return ir.Errors{ir.DefinitionError(d, 0, 0, "", "is not a directory; remove it (bob keeps its own files there)")}
		}
	}
	return nil
}
