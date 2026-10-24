package bop

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A small pkg-config: bob resolves the compile and link flags of nixpkgs libraries and
// Externals inside the derivation, where their outputs exist, from the .pc files they install.
// It reads Cflags, Libs, Requires and Requires.private with variable expansion; that is all
// the libraries' files use (pkg-config's own behaviour for shared linking: Requires.private
// contributes Cflags only).

// pcFlags are the flags of one PkgConfig entry.
type pcFlags struct{ cflags, libs []string }

// maxPCFile bounds one .pc file; maxPropagated bounds the propagated packages followed.
const (
	maxPCFile     = 1 << 20
	maxPropagated = 256
)

// resolve returns the flags of pc.
func resolve(pc PkgConfig) (pcFlags, error) {
	direct, all, err := pcIndex(pc.Roots)
	if err != nil {
		return pcFlags{}, err
	}
	mods := pc.Modules
	if len(mods) == 0 {
		mods = direct
	}
	if len(mods) == 0 {
		return fallback(pc)
	}
	r := &pcResolver{files: all, seen: map[string]bool{}, vars: map[string]map[string]string{}}
	var f pcFlags
	for _, m := range mods {
		if err := r.module(m, true, &f, 0); err != nil {
			return pcFlags{}, err
		}
	}
	return pcFlags{cflags: dedupeFirst(f.cflags), libs: withRPath(dedupeLibs(f.libs))}, nil
}

// fallback gives -I for the include dirs and -L/-l for every library of LibDir.
func fallback(pc PkgConfig) (pcFlags, error) {
	var f pcFlags
	for _, d := range pc.Include {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			f.cflags = append(f.cflags, "-I"+d)
		}
	}
	if pc.LibDir == "" {
		return f, nil
	}
	ents, err := os.ReadDir(pc.LibDir)
	if err != nil {
		return pcFlags{}, fmt.Errorf("library directory %s: %w", pc.LibDir, err)
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "lib") {
			continue
		}
		base, ok := strings.CutSuffix(n, ".so")
		if !ok {
			base, ok = strings.CutSuffix(n, ".a")
		}
		if ok && len(base) > 3 {
			names = append(names, strings.TrimPrefix(base, "lib"))
		}
	}
	if len(names) == 0 {
		return pcFlags{}, fmt.Errorf("%s has no pkg-config file and no lib*.so or lib*.a library", pc.LibDir)
	}
	f.libs = append(f.libs, "-L"+pc.LibDir)
	for _, n := range sortedUnique(names) {
		f.libs = append(f.libs, "-l"+n)
	}
	f.libs = withRPath(f.libs)
	return f, nil
}

// pcIndex finds the .pc files of roots and of the packages they propagate: the module names
// of roots themselves (sorted) and every module → file.
func pcIndex(roots []string) ([]string, map[string]string, error) {
	all := map[string]string{}
	var direct []string
	queue := slices.Clone(roots)
	seen := map[string]bool{}
	for i := 0; i < len(queue) && len(seen) < maxPropagated; i++ {
		root := queue[i]
		if seen[root] {
			continue
		}
		seen[root] = true
		for _, sub := range []string{"lib/pkgconfig", "share/pkgconfig"} {
			ents, err := os.ReadDir(filepath.Join(root, sub))
			if err != nil {
				continue
			}
			for _, e := range ents {
				m, ok := strings.CutSuffix(e.Name(), ".pc")
				if !ok || m == "" {
					continue
				}
				if _, dup := all[m]; !dup {
					all[m] = filepath.Join(root, sub, e.Name())
					if i < len(roots) {
						direct = append(direct, m)
					}
				}
			}
		}
		b, err := os.ReadFile(filepath.Join(root, "nix-support", "propagated-build-inputs"))
		if err == nil {
			queue = append(queue, strings.Fields(string(b))...)
		}
	}
	return sortedUnique(direct), all, nil
}

type pcResolver struct {
	files map[string]string
	seen  map[string]bool
	vars  map[string]map[string]string
}

// module adds module m's flags to f: its own, then those of the modules it requires. public
// is false below Requires.private: those contribute Cflags only.
func (r *pcResolver) module(m string, public bool, f *pcFlags, depth int) error {
	key := fmt.Sprintf("%s/%t", m, public)
	if r.seen[key] || depth > 32 {
		return nil
	}
	r.seen[key] = true
	file, ok := r.files[m]
	if !ok {
		return fmt.Errorf("pkg-config module %q not found", m)
	}
	fields, err := parsePC(file)
	if err != nil {
		return err
	}
	f.cflags = append(f.cflags, splitArgs(fields["Cflags"])...)
	if public {
		f.libs = append(f.libs, splitArgs(fields["Libs"])...)
	}
	for _, req := range requires(fields["Requires"]) {
		if err := r.module(req, public, f, depth+1); err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
	}
	for _, req := range requires(fields["Requires.private"]) {
		if err := r.module(req, false, f, depth+1); err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
	}
	return nil
}

// parsePC reads the fields of a .pc file with its variables expanded.
func parsePC(file string) (map[string]string, error) {
	fh, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	vars := map[string]string{"pcfiledir": filepath.Dir(file)}
	fields := map[string]string{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), maxPCFile)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i <= 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:i]), expand(strings.TrimSpace(line[i+1:]), vars)
		if line[i] == '=' {
			vars[k] = v
		} else {
			fields[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return fields, nil
}

// expand replaces ${name} by the variable's value (unknown names by "").
func expand(s string, vars map[string]string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(vars[s[i+2:i+j]])
		s = s[i+j+1:]
	}
}

// requires lists the module names of a Requires field ("a >= 1.2, b c").
func requires(s string) []string {
	var out []string
	toks := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "=", "<", ">", "<=", ">=", "!=":
			i++ // skip the version
			continue
		}
		out = append(out, toks[i])
	}
	return out
}

// splitArgs splits a flags field like a shell would (quotes and backslashes).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote, esc := false, rune(0), false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc, in = false, true
		case r == '\\' && quote != '\'':
			esc = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// dedupeFirst keeps the first occurrence of each flag.
func dedupeFirst(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// dedupeLibs keeps the first -L of each directory and the last occurrence of every other flag
// (a library must follow the libraries that use it).
func dedupeLibs(s []string) []string {
	last := map[string]int{}
	for i, x := range s {
		last[x] = i
	}
	seen := map[string]bool{}
	var out []string
	for i, x := range s {
		if strings.HasPrefix(x, "-L") {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
			continue
		}
		if last[x] == i {
			out = append(out, x)
		}
	}
	return out
}

// withRPath adds a RUNPATH entry for every -L store directory, so programs run under env -i.
func withRPath(libs []string) []string {
	out := slices.Clone(libs)
	for _, x := range libs {
		if d, ok := strings.CutPrefix(x, "-L"); ok && strings.HasPrefix(d, "/nix/store/") {
			out = append(out, "-Wl,-rpath,"+d)
		}
	}
	return out
}
