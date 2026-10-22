// Package bop holds the bob-builder kind "tarball" (SPEC R-OUT-004): a reproducible
// <name>-<platform>.tar.gz of a closure or of one static executable (tar and gzip rules of
// R-OUT-010).
package bop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
)

// Args is the Args schema of kind "tarball".
type Args struct {
	File     string   `json:"file"`     // <name>-<platform>.tar.gz
	Kind     string   `json:"kind"`     // closure | static
	Paths    []string `json:"paths"`    // closure: every store path of the closure
	Roots    []string `json:"roots"`    // closure: the roots, listed in bob/roots
	Program  string   `json:"program"`  // static: the executable (absolute store path of the file)
	Name     string   `json:"name"`     // static: the entry bin/<name>
	Licences string   `json:"licences"` // content of bob/licences.json
}

// Kinds is the tarball kind; its output must stay byte-exact.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{"tarball": {Run: tarballKind, NoFixup: true}}
}

func tarballKind(c *builder.Ctx, raw json.RawMessage) error {
	var a Args
	if err := builder.DecodeArgs(raw, &a); err != nil {
		return err
	}
	if a.Kind == "unplannable" {
		// bob plans this job when the program could not be built while planning; it waits for
		// the program, whose failure the build reports, and only runs if that build succeeds.
		return errors.New("the program of the tarball could not be built while planning")
	}
	if a.File == "" || strings.ContainsRune(a.File, '/') || !strings.HasSuffix(a.File, ".tar.gz") {
		return builder.Invalidf("tarball file name %q", a.File)
	}
	entries := []builder.TarEntry{
		builder.TarDir("bob", 0o755),
		builder.TarFile("bob/licences.json", 0o444, []byte(a.Licences)),
	}
	switch a.Kind {
	case "static":
		if a.Name == "" || strings.ContainsRune(a.Name, '/') || !strings.HasPrefix(a.Program, "/nix/store/") {
			return builder.Invalidf("static tarball: name %q, program %q", a.Name, a.Program)
		}
		file := filepath.Join(c.StoreDir, strings.TrimPrefix(a.Program, "/nix/store/"))
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", a.Program)
		}
		entries = append(entries, builder.TarDir("bin", 0o755),
			builder.TarEntry{Name: "bin/" + a.Name, Mode: int64(info.Mode().Perm()), Type: '0', File: file, Size: info.Size()})
	case "closure":
		entries = append(entries, builder.TarDir("nix", 0o555), builder.TarDir("nix/store", 0o555),
			builder.TarFile("bob/roots", 0o444, []byte(strings.Join(a.Roots, "\n")+"\n")))
		for _, p := range a.Paths {
			base := filepath.Base(p)
			if !strings.HasPrefix(p, "/nix/store/") || strings.Contains(strings.TrimPrefix(p, "/nix/store/"), "/") {
				return builder.Invalidf("%q is not a store path", p)
			}
			es, err := builder.TreeEntries(filepath.Join(c.StoreDir, base), "nix/store/"+base, nil)
			if err != nil {
				return err
			}
			entries = append(entries, es...)
		}
	default:
		return builder.Invalidf("tarball kind %q", a.Kind)
	}
	out := c.Outputs["out"]
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	c.Phase("tarball")
	_, err := builder.WriteTarGz(filepath.Join(out, a.File), entries)
	return err
}
