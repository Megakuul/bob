package gen

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// maxEmbedFiles bounds the files one package may embed.
const maxEmbedFiles = 100_000

// embedSet is a package's resolved //go:embed patterns: pattern → files (relative to the
// package directory, slash-separated), and the union of files.
type embedSet struct {
	patterns map[string][]string
	files    []string
}

// resolveEmbeds applies the go command's //go:embed rules to patterns in dir: a pattern names
// files or directories (path.Match per element); a directory embeds its files recursively
// except those starting with "." or "_" (unless the pattern starts with "all:"); a pattern
// must match something, may not leave the package and may not name irregular files.
func resolveEmbeds(dir string, patterns []string) (*embedSet, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	es := &embedSet{patterns: map[string][]string{}}
	all := map[string]bool{}
	for _, pat := range patterns {
		glob, keepHidden := strings.CutPrefix(pat, "all:")
		if glob == "" || path.IsAbs(glob) || strings.Contains(glob, "\\") || path.Clean(glob) != glob ||
			glob == ".." || strings.HasPrefix(glob, "../") {
			return nil, fmt.Errorf("//go:embed %s: invalid pattern", pat)
		}
		matches, err := fs.Glob(os.DirFS(dir), glob)
		if err != nil {
			return nil, fmt.Errorf("//go:embed %s: %w", pat, err)
		}
		var files []string
		for _, m := range matches {
			fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(m)))
			if err != nil {
				return nil, err
			}
			switch {
			case fi.Mode().IsRegular():
				files = append(files, m)
			case fi.IsDir():
				err := filepath.WalkDir(filepath.Join(dir, filepath.FromSlash(m)), func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					rel, _ := filepath.Rel(dir, p)
					rel = filepath.ToSlash(rel)
					name := d.Name()
					if rel != m && !keepHidden && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
					if d.IsDir() {
						if rel != m {
							if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
								return filepath.SkipDir // another module
							}
						}
						return nil
					}
					if !d.Type().IsRegular() {
						return fmt.Errorf("//go:embed %s: %s is not a regular file", pat, rel)
					}
					files = append(files, rel)
					if len(files) > maxEmbedFiles {
						return fmt.Errorf("//go:embed %s: more than %d files", pat, maxEmbedFiles)
					}
					return nil
				})
				if err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("//go:embed %s: %s is not a regular file", pat, m)
			}
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("//go:embed %s: no matching files found", pat)
		}
		sort.Strings(files)
		es.patterns[pat] = files
		for _, f := range files {
			all[f] = true
		}
	}
	for f := range all {
		es.files = append(es.files, f)
	}
	sort.Strings(es.files)
	return es, nil
}
