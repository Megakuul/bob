package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/build"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

// Caches of what planning reads from store paths (B-049). A store path never changes once it
// is valid (module trees are content-addressed by the lock's tree hash, S2-2; generated code
// is imported by content, §03.6.3), so what bob derives from reading one is a function of the
// path: the package list of a vendored module tree (treePackages) and go/build's view of a
// package directory in one configuration (importDir). Entries live in the user cache directory
// ($XDG_CACHE_HOME/bob/…, like the evaluator seed and the npm metadata cache) and are written
// atomically; a missing, damaged or foreign entry is recomputed, and failing to write one only
// costs the read next time. Results that do not survive the encoding exactly are not cached.

// treeCacheVersion is part of every entry; a change of what is cached or of how it is computed
// must change it.
const treeCacheVersion = "bob-go-tree-cache-v2"

// maxTreeCacheEntry bounds the bytes read of one entry.
const maxTreeCacheEntry = 16 << 20

// treeCacheFile is the entry name in the user cache directory's bob/<kind>, "" when there is
// no user cache directory.
func treeCacheFile(kind, name string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "bob", kind, name)
}

// readTreeCache reads an entry, nil when it is missing or too large.
func readTreeCache(file string) []byte {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTreeCacheEntry+1))
	if err != nil || len(b) > maxTreeCacheEntry {
		return nil
	}
	return b
}

// writeTreeCache writes an entry atomically (temporary file and rename), so a concurrent bob
// reads either nothing or a whole entry.
func writeTreeCache(file string, b []byte) error {
	if len(b) > maxTreeCacheEntry {
		return fmt.Errorf("cache entry of %d bytes", len(b))
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// treePackages is modulePackages of the module tree at store path sp (read at host), cached
// per tree: walking every vendored tree was most of a no-op `bob gen` (B-049). The entry is
// named by the host path, which holds the store path (and the store's home, so stores of
// different homes never share an entry).
func treePackages(sp, host, modPath string) ([]string, error) {
	file := ""
	if strings.HasPrefix(sp, "/nix/store/") && strings.HasSuffix(host, sp) {
		h := sha256.Sum256([]byte(host))
		file = treeCacheFile("go-packages", hex.EncodeToString(h[:]))
	}
	if file != "" {
		if pkgs, ok := decodePackages(readTreeCache(file), modPath); ok {
			return pkgs, nil
		}
	}
	pkgs, err := modulePackages(host, modPath)
	if err != nil || file == "" {
		return pkgs, err
	}
	if b, ok := encodePackages(modPath, pkgs); ok {
		_ = writeTreeCache(file, b)
	}
	return pkgs, nil
}

// encodePackages is the entry of a package list: the version line, the module path, one
// import path per line.
func encodePackages(modPath string, pkgs []string) ([]byte, bool) {
	var b strings.Builder
	b.WriteString(treeCacheVersion + "\n" + modPath + "\n")
	for _, ip := range pkgs {
		if strings.Contains(ip, "\n") {
			return nil, false
		}
		b.WriteString(ip + "\n")
	}
	return []byte(b.String()), !strings.Contains(modPath, "\n")
}

// decodePackages returns the package list of an entry when it is well-formed for modPath:
// strictly sorted import paths within the module, as modulePackages returns them.
func decodePackages(b []byte, modPath string) ([]string, bool) {
	lines := strings.Split(string(b), "\n")
	if len(lines) < 3 || lines[0] != treeCacheVersion || lines[1] != modPath || lines[len(lines)-1] != "" {
		return nil, false
	}
	pkgs := lines[2 : len(lines)-1]
	if len(pkgs) > maxModulePackages {
		return nil, false
	}
	for i, ip := range pkgs {
		if !within(ip, modPath) || i > 0 && pkgs[i-1] >= ip {
			return nil, false
		}
	}
	if len(pkgs) == 0 {
		return nil, true // modulePackages' answer for a tree without packages
	}
	return pkgs, true
}

// slimPackage is the part of a go/build package the planner uses (the file lists, imports,
// embed patterns, cgo directives, name and directory). Packages read from store paths are
// always slimmed, cached or not, so the planner sees the same package either way; the position
// maps and directives it does not read were most of the decoding cost of an entry.
func slimPackage(bp *build.Package) *build.Package {
	return &build.Package{Dir: bp.Dir, Name: bp.Name, ImportPath: bp.ImportPath,
		GoFiles: bp.GoFiles, CgoFiles: bp.CgoFiles, IgnoredGoFiles: bp.IgnoredGoFiles,
		CFiles: bp.CFiles, CXXFiles: bp.CXXFiles, MFiles: bp.MFiles, HFiles: bp.HFiles, FFiles: bp.FFiles,
		SFiles: bp.SFiles, SwigFiles: bp.SwigFiles, SwigCXXFiles: bp.SwigCXXFiles, SysoFiles: bp.SysoFiles,
		CgoCFLAGS: bp.CgoCFLAGS, CgoLDFLAGS: bp.CgoLDFLAGS, CgoPkgConfig: bp.CgoPkgConfig,
		TestGoFiles: bp.TestGoFiles, XTestGoFiles: bp.XTestGoFiles,
		Imports: bp.Imports, TestImports: bp.TestImports, XTestImports: bp.XTestImports,
		EmbedPatterns: bp.EmbedPatterns, TestEmbedPatterns: bp.TestEmbedPatterns, XTestEmbedPatterns: bp.XTestEmbedPatterns}
}

// buildEntry is a cached go/build package: the key it was computed for and the result.
type buildEntry struct {
	Key     string
	Package *build.Package
}

// buildKey identifies one go/build read of the directory dir: everything of the context that
// importDir does not fix (goContext), the mode, the Go version bob is built with (go/build's
// rules), and the directory.
func buildKey(bc *build.Context, dir string) string {
	k, _ := json.Marshal([]any{treeCacheVersion, runtime.Version(), "ImportComment", bc.GOOS, bc.GOARCH,
		bc.CgoEnabled, bc.UseAllFiles, bc.Compiler, bc.BuildTags, bc.ToolTags, bc.ReleaseTags, bc.InstallSuffix,
		bc.GOROOT, bc.GOPATH, bc.Dir, dir})
	return string(k)
}

// buildCacheFile is the entry of key.
func buildCacheFile(key string) string {
	h := sha256.Sum256([]byte(key))
	return treeCacheFile("go-build", hex.EncodeToString(h[:]))
}

// readBuildCache returns the cached package of key.
func readBuildCache(file, key string) (*build.Package, bool) {
	b := readTreeCache(file)
	if b == nil {
		return nil, false
	}
	var e buildEntry
	if err := json.Unmarshal(b, &e); err != nil || e.Key != key || e.Package == nil {
		return nil, false
	}
	return e.Package, true
}

// writeBuildCache caches bp for key when it decodes back to exactly bp (encoding/json would
// replace invalid UTF-8 in a hostile tree's comments, for example).
func writeBuildCache(file, key string, bp *build.Package) error {
	b, err := json.Marshal(buildEntry{Key: key, Package: bp})
	if err != nil {
		return err
	}
	var back buildEntry
	if err := json.Unmarshal(b, &back); err != nil {
		return err
	}
	if back.Key != key || !reflect.DeepEqual(back.Package, bp) {
		return fmt.Errorf("package %s does not survive the cache encoding", bp.Dir)
	}
	return writeTreeCache(file, b)
}
