package resolve

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
)

// Bounds for the repository snapshot (hostile input, AGENTS.md §5.4).
const (
	maxNameLen        = 128
	maxVersionsPerPkg = 4096
	maxPackagesLoaded = 50000
)

// ErrBadName is returned for package names opam would not accept.
var ErrBadName = errors.New("invalid opam package name")

// ValidName reports whether s is an opam package name (letters, digits and "-_+", not starting
// with "-", at most 128 bytes).
func ValidName(s string) bool {
	if s == "" || len(s) > maxNameLen || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '+') {
			return false
		}
	}
	return true
}

// ValidVersion reports whether s is an opam version (letters, digits and "-+.~_", at most
// 128 bytes).
func ValidVersion(s string) bool {
	if s == "" || len(s) > maxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' || c == '~' || c == '_') {
			return false
		}
	}
	return true
}

// Version is one version of one package in the repository.
type Version struct {
	Name, Version string
	File          *File  // nil when the opam file does not parse (ParseErr says why)
	ParseErr      error  // the version is then never installable
	Sum           string // sha256 hex of the opam file
}

// Repository reads an unpacked opam-repository snapshot (packages/<name>/<name>.<version>/opam).
// Opam files are read and parsed on first use and cached; safe for concurrent use.
type Repository struct {
	fsys fs.FS

	mu     sync.Mutex
	cache  map[string][]*Version
	loaded int
}

// NewRepository returns a repository over fsys, whose root holds the "packages" directory.
func NewRepository(fsys fs.FS) *Repository {
	return &Repository{fsys: fsys, cache: map[string][]*Version{}}
}

// Versions returns every version of name, newest first; an unknown package has none.
func (r *Repository) Versions(name string) ([]*Version, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q", ErrBadName, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if vs, ok := r.cache[name]; ok {
		return vs, nil
	}
	vs, err := r.load(name)
	if err != nil {
		return nil, err
	}
	r.cache[name] = vs
	return vs, nil
}

func (r *Repository) load(name string) ([]*Version, error) {
	dir := path.Join("packages", name)
	ents, err := fs.ReadDir(r.fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read opam repository %s: %w", dir, err)
	}
	if len(ents) > maxVersionsPerPkg {
		return nil, fmt.Errorf("opam repository: %s has %d versions, more than %d", name, len(ents), maxVersionsPerPkg)
	}
	var vs []*Version
	for _, e := range ents {
		ver, ok := strings.CutPrefix(e.Name(), name+".")
		if !e.IsDir() || !ok || !ValidVersion(ver) {
			continue
		}
		if r.loaded++; r.loaded > maxPackagesLoaded {
			return nil, fmt.Errorf("opam repository: more than %d package versions read", maxPackagesLoaded)
		}
		v, err := r.readVersion(path.Join(dir, e.Name()), name, ver)
		if err != nil {
			return nil, err
		}
		if v != nil {
			vs = append(vs, v)
		}
	}
	// Versions that differ only in zero digit runs ("1+x" and "1+x0") compare equal; the
	// text decides between them so the order is total and deterministic.
	slices.SortFunc(vs, func(a, b *Version) int {
		if c := CompareVersions(b.Version, a.Version); c != 0 {
			return c
		}
		return strings.Compare(b.Version, a.Version)
	})
	return vs, nil
}

func (r *Repository) readVersion(dir, name, ver string) (*Version, error) {
	ents, err := fs.ReadDir(r.fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read opam repository %s: %w", dir, err)
	}
	for _, e := range ents {
		if e.Name() != "opam" {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, nil // a symlink or special file is never followed
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("read opam repository %s/opam: %w", dir, err)
		}
		v := &Version{Name: name, Version: ver}
		if info.Size() > maxFileBytes {
			v.ParseErr = fmt.Errorf("%w: opam file larger than %d bytes", ErrSyntax, maxFileBytes)
			return v, nil
		}
		b, err := fs.ReadFile(r.fsys, dir+"/opam")
		if err != nil {
			return nil, fmt.Errorf("read opam repository %s/opam: %w", dir, err)
		}
		sum := sha256.Sum256(b)
		v.Sum = hex.EncodeToString(sum[:])
		v.File, v.ParseErr = Parse(b)
		return v, nil
	}
	return nil, nil
}
