package resolve

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Limits on a .crate archive read for its manifest (hostile input).
const (
	maxCrateEntries   = 100000
	maxCrateUnpacked  = 1 << 30
	maxManifestInFile = maxTOML
)

// MetaFromCrate reads the Cargo.toml facts of a .crate (gzip tar with the single top
// directory <name>-<version>/). The archive must already be verified against its checksum.
func MetaFromCrate(name, version string, r io.Reader) (CrateMeta, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return CrateMeta{}, fmt.Errorf("%s %s: %w", name, version, err)
	}
	tr := tar.NewReader(io.LimitReader(gz, maxCrateUnpacked))
	prefix := name + "-" + version + "/"
	var manifest []byte
	files := map[string]bool{}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return CrateMeta{}, fmt.Errorf("%s %s: reading crate: %w", name, version, err)
		}
		if n >= maxCrateEntries {
			return CrateMeta{}, fmt.Errorf("%s %s: crate has more than %d entries", name, version, maxCrateEntries)
		}
		rel, ok := strings.CutPrefix(path.Clean(h.Name), prefix)
		if !ok {
			continue
		}
		files[rel] = true
		if rel == "Cargo.toml" && h.Typeflag == tar.TypeReg {
			if manifest, err = io.ReadAll(io.LimitReader(tr, maxManifestInFile+1)); err != nil {
				return CrateMeta{}, fmt.Errorf("%s %s: %w", name, version, err)
			}
		}
	}
	if manifest == nil {
		return CrateMeta{}, fmt.Errorf("%s %s: the crate has no Cargo.toml", name, version)
	}
	return MetaFromManifest(name, manifest, func(rel string) bool { return files[rel] })
}

// MetaFromManifest extracts the planning facts from a published Cargo.toml; exists reports
// whether a file (crate-relative, '/'-separated) is in the crate.
func MetaFromManifest(name string, cargoToml []byte, exists func(string) bool) (CrateMeta, error) {
	doc, err := parseTOML(string(cargoToml))
	if err != nil {
		return CrateMeta{}, fmt.Errorf("%s: Cargo.toml: %w", name, err)
	}
	pkg, _ := doc["package"].(map[string]any)
	if pkg == nil {
		pkg, _ = doc["project"].(map[string]any) // pre-2018 spelling
	}
	if pkg == nil {
		return CrateMeta{}, fmt.Errorf("%s: Cargo.toml has no [package]", name)
	}
	m := CrateMeta{Edition: "2015"}
	if e, ok := pkg["edition"].(string); ok {
		m.Edition = e
	}
	switch b := pkg["build"].(type) {
	case string:
		m.Build = b
	case bool:
		if b && exists("build.rs") {
			m.Build = "build.rs"
		}
	case nil:
		if exists("build.rs") {
			m.Build = "build.rs"
		}
	default:
		return CrateMeta{}, fmt.Errorf("%s: Cargo.toml: package.build is neither a path nor a boolean", name)
	}
	libPath, libName := "src/lib.rs", ""
	if lib, ok := doc["lib"].(map[string]any); ok {
		if s, ok := lib["name"].(string); ok {
			libName = s
		}
		if s, ok := lib["path"].(string); ok {
			libPath = s
		}
		pm1, _ := lib["proc-macro"].(bool)
		pm2, _ := lib["proc_macro"].(bool)
		m.ProcMacro = pm1 || pm2
		if e, ok := lib["edition"].(string); ok {
			m.Edition = e
		}
	} else if auto, ok := pkg["autolib"].(bool); ok && !auto {
		return CrateMeta{}, fmt.Errorf("%s: the crate has no library target", name)
	}
	if libName != "" && libName != strings.ReplaceAll(name, "-", "_") {
		m.LibName = libName
	}
	if libPath = strings.TrimPrefix(libPath, "./"); libPath != "src/lib.rs" {
		m.LibPath = libPath
	}
	if !exists(path.Clean(libPath)) {
		return CrateMeta{}, fmt.Errorf("%s: the crate has no library target (%s is missing)", name, libPath)
	}
	for what, p := range map[string]string{"build script": m.Build, "library path": m.LibPath} {
		if p != "" && !safeRel(p) {
			return CrateMeta{}, fmt.Errorf("%s: %s %q leaves the crate", name, what, p)
		}
	}
	switch m.Edition {
	case "2015", "2018", "2021", "2024":
	default:
		return CrateMeta{}, fmt.Errorf("%s: unknown edition %q", name, m.Edition)
	}
	if m.LibName != "" && !ValidName(m.LibName) {
		return CrateMeta{}, fmt.Errorf("%s: invalid library name %q", name, m.LibName)
	}
	return m, nil
}

func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
		return false
	}
	c := path.Clean(p)
	return c != ".." && !strings.HasPrefix(c, "../")
}
