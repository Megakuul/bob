// Package pcflags is bob's small pkg-config for nixpkgs libraries and Externals (an E0 engine
// primitive: the builder and the generators may both import it, R-ARCH-003). The builder
// (pkg/cc/bop) resolves the flags of every library inside the compile job, where the outputs
// exist; the generator (pkg/cc/gen) resolves the same flags for compile_commands.json from the
// outputs it can read (A564), so the build, the dev shell's clangd and the flags of
// compile_commands.json come from one implementation.
package pcflags

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

// Flags are the flags of one PkgConfig entry.
type Flags struct{ Cflags, Libs []string }

// Config describes one library: where it is and which of its modules to use.
type Config struct {
	// Roots are the library's store outputs. Its .pc files are in <root>/lib/pkgconfig and
	// <root>/share/pkgconfig, and in those of the packages listed in
	// <root>/nix-support/propagated-build-inputs.
	Roots   []string
	Modules []string // the modules to use; empty = every .pc file of the roots
	// Include and LibDir are used besides the .pc files: -I for each existing Include (and
	// for the include directory of every root), and -L LibDir with -l for every lib*.so and
	// lib*.a in it when no module names a library.
	Include []string
	LibDir  string
	// Host maps a path of the flags to where the caller reads it (nil = the path itself). The
	// builder runs inside bob's namespace and reads the paths as they are; the generator reads
	// below the store's home and still writes the in-namespace paths into the flags.
	Host func(string) string
}

func (c Config) host(p string) string {
	if c.Host == nil {
		return p
	}
	return c.Host(p)
}

// maxPCFile bounds one .pc file; maxPropagated bounds the propagated packages followed.
const (
	maxPCFile     = 1 << 20
	maxPropagated = 256
)

// Resolve returns the flags of pc. The .pc files give the flags they declare; a library is
// never left without its headers and libraries because its .pc file is thin: nixpkgs' boost.pc
// names only includedir and libdir, and many header-only libraries have no .pc at all. So the
// include directories of the library's outputs and of the packages they propagate are always
// added, and when no module names a library, every lib*.so / lib*.a of LibDir is linked
// (only as far as the program uses it, --as-needed).
func Resolve(pc Config) (Flags, error) {
	direct, all, visited, err := pcIndex(pc.Roots, pc.host)
	if err != nil {
		return Flags{}, err
	}
	mods := pc.Modules
	if len(mods) == 0 {
		mods = direct
	}
	var f Flags
	if len(mods) > 0 {
		r := newPCResolver(all, pc.host)
		for _, m := range mods {
			if err := r.module(m, true, &f, 0); err != nil {
				return Flags{}, err
			}
		}
		f = Flags{Cflags: dedupeFirst(f.Cflags), Libs: withRPath(dedupeLibs(f.Libs))}
	}
	f.Cflags = withIncludes(f.Cflags, pc.Include, visited, pc.host)
	if !linksLibrary(f.Libs) {
		libs, err := dirLibs(pc.LibDir, pc.host)
		if err != nil {
			return Flags{}, err
		}
		f.Libs = append(f.Libs, libs...)
	}
	if len(f.Cflags) == 0 && len(f.Libs) == 0 {
		return Flags{}, fmt.Errorf("%s has no pkg-config module, no include directory and no lib*.so or lib*.a library: it provides nothing to compile or link against (a program belongs in Tool, not Lib)", strings.Join(pc.Roots, ", "))
	}
	return f, nil
}

// withIncludes adds -I for every existing include directory of the library (include) and of
// the outputs visited through propagated-build-inputs that the flags do not name yet.
func withIncludes(cflags, include, visited []string, host func(string) string) []string {
	have := map[string]bool{}
	for _, c := range cflags {
		have[c] = true
	}
	dirs := slices.Clone(include)
	for _, root := range visited {
		dirs = append(dirs, filepath.Join(root, "include"))
	}
	out := slices.Clone(cflags)
	for _, d := range dirs {
		fi, err := os.Stat(host(d))
		if err != nil || !fi.IsDir() || have["-I"+d] {
			continue
		}
		have["-I"+d] = true
		out = append(out, "-I"+d)
	}
	return out
}

// linksLibrary reports whether flags name a library to link.
func linksLibrary(libs []string) bool {
	for _, l := range libs {
		if strings.HasPrefix(l, "-l") || strings.HasSuffix(l, ".a") || strings.HasSuffix(l, ".so") {
			return true
		}
	}
	return false
}

// dirLibs links every library of dir, as far as the program uses it: a library the program
// needs nothing from adds no dependency. A directory without libraries (a header-only
// library) adds nothing.
func dirLibs(dir string, host func(string) string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(host(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("library directory %s: %w", dir, err)
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
		return nil, nil
	}
	libs := []string{"-Wl,--push-state,--as-needed", "-L" + dir}
	for _, n := range sortedUnique(names) {
		libs = append(libs, "-l"+n)
	}
	libs = append(libs, "-Wl,--pop-state")
	return withRPath(libs), nil
}

// pcIndex finds the .pc files of roots and of the packages they propagate: the module names
// of roots themselves (sorted), every module → file, and the roots visited (the given ones,
// then the propagated ones).
func pcIndex(roots []string, host func(string) string) ([]string, map[string]string, []string, error) {
	all := map[string]string{}
	var direct []string
	queue := slices.Clone(roots)
	seen := map[string]bool{}
	var visited []string
	for i := 0; i < len(queue) && len(seen) < maxPropagated; i++ {
		root := queue[i]
		if seen[root] {
			continue
		}
		seen[root] = true
		visited = append(visited, root)
		for _, sub := range []string{"lib/pkgconfig", "share/pkgconfig"} {
			ents, err := os.ReadDir(host(filepath.Join(root, sub)))
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
		b, err := os.ReadFile(host(filepath.Join(root, "nix-support", "propagated-build-inputs")))
		if err == nil {
			queue = append(queue, strings.Fields(string(b))...)
		}
	}
	return sortedUnique(direct), all, visited, nil
}

type pcResolver struct {
	files map[string]string
	host  func(string) string
	memo  map[string]Flags // module/public → its flags followed by those of what it requires
	open  map[string]bool  // modules being resolved (a Requires cycle ends there)
}

func newPCResolver(files map[string]string, host func(string) string) *pcResolver {
	return &pcResolver{files: files, host: host, memo: map[string]Flags{}, open: map[string]bool{}}
}

// module adds module m's flags to f: its own, then those of the modules it requires. public
// is false below Requires.private: those contribute Cflags only. A module that was added
// before is added again after its dependents, so that dedupeLibs, which keeps the last
// occurrence of a library, puts it behind everything that needs it (a static libCatch2Main.a
// requires libCatch2.a, and catch2.pc may be listed before catch2-with-main.pc).
func (r *pcResolver) module(m string, public bool, f *Flags, depth int) error {
	got, err := r.flags(m, public, depth)
	if err != nil {
		return err
	}
	f.Cflags = append(f.Cflags, got.Cflags...)
	f.Libs = append(f.Libs, got.Libs...)
	return nil
}

func (r *pcResolver) flags(m string, public bool, depth int) (Flags, error) {
	key := fmt.Sprintf("%s/%t", m, public)
	if got, ok := r.memo[key]; ok {
		return got, nil
	}
	if r.open[key] || depth > 32 {
		return Flags{}, nil
	}
	r.open[key] = true
	defer delete(r.open, key)
	file, ok := r.files[m]
	if !ok {
		return Flags{}, fmt.Errorf("pkg-config module %q not found", m)
	}
	fields, err := parsePC(file, r.host)
	if err != nil {
		return Flags{}, err
	}
	f := Flags{Cflags: splitArgs(fields["Cflags"])}
	if public {
		f.Libs = splitArgs(fields["Libs"])
	}
	for _, q := range []struct {
		field  string
		public bool
	}{{"Requires", public}, {"Requires.private", false}} {
		for _, req := range requires(fields[q.field]) {
			sub, err := r.flags(req, q.public, depth+1)
			if err != nil {
				return Flags{}, fmt.Errorf("%s: %w", m, err)
			}
			f.Cflags = append(f.Cflags, sub.Cflags...)
			f.Libs = append(f.Libs, sub.Libs...)
		}
	}
	r.memo[key] = f
	return f, nil
}

// parsePC reads the fields of a .pc file with its variables expanded.
func parsePC(file string, host func(string) string) (map[string]string, error) {
	fh, err := os.Open(host(file))
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

// sortedUnique returns s sorted without duplicates.
func sortedUnique(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}
