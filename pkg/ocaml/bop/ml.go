package bop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
)

var libNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// compileKind is kind ml-compile: one local library into <out>/lib/<name>.
func compileKind(c *builder.Ctx, raw json.RawMessage) error {
	var a CompileArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !libNameRe.MatchString(a.Name) || !filepath.IsAbs(a.Out) {
		return builder.Invalidf("ml-compile: name %q / out %q is invalid", a.Name, a.Out)
	}
	if err := checkCommon(a.Src, a.Dir, a.Modules, a.Stubs); err != nil {
		return err
	}
	r, err := newRunner(c, a.Path, a.Env)
	if err != nil {
		return err
	}
	r.dir = a.Dir
	work, err := copySources(c, a.Src, a.Dir)
	if err != nil {
		return err
	}
	dest := filepath.Join(a.Out, "lib", a.Name)
	if err := c.MkdirAll(dest); err != nil {
		return err
	}
	if a.Dune {
		return duneLibrary(c, r, a, work)
	}
	c.Phase("ocamldep")
	order, err := depOrder(r, work, a.Modules)
	if err != nil {
		return err
	}
	c.Phase("compile")
	pkgs := packageFlags(a.Packages)
	var cmx []string
	if a.Wrapped {
		alias, err := writeAlias(work, a.Name, order)
		if err != nil {
			return err
		}
		if err := r.run(work, "ocamlfind", "ocamlopt", "-c", "-g", "-bin-annot", "-w", "-49", "-no-alias-deps", alias); err != nil {
			return err
		}
		cmx = append(cmx, strings.TrimSuffix(alias, ".ml")+".cmx")
		if !slices.Contains(order, a.Name+".ml") && !slices.Contains(order, a.Name+".mli") {
			// No main module: the library's module is the alias module itself (as dune).
			main := a.Name + ".ml"
			if err := copyFile(filepath.Join(work, alias), filepath.Join(work, main)); err != nil {
				return err
			}
			order = append(order, main)
		}
	}
	for _, f := range order {
		base := moduleBase(f)
		argv := append([]string{"ocamlfind", "ocamlopt"}, pkgs...)
		argv = append(argv, "-c", "-g", "-bin-annot")
		prefix := base
		if a.Wrapped {
			argv = append(argv, "-open", capital(a.Name)+"__")
			if base != a.Name {
				prefix = a.Name + "__" + capital(base)
			}
		}
		argv = append(argv, a.Flags...)
		argv = append(argv, "-o", prefix, f)
		if err := r.run(work, argv...); err != nil {
			return err
		}
		if strings.HasSuffix(f, ".ml") {
			cmx = append(cmx, prefix+".cmx")
		}
	}
	archive := []string{"ocamlfind", "ocamlopt", "-a", "-o", a.Name + ".cmxa"}
	archive = append(archive, cmx...)
	if len(a.Stubs) > 0 {
		c.Phase("stubs")
		var objs []string
		for _, s := range a.Stubs {
			argv := []string{"ocamlfind", "ocamlopt", "-c", "-ccopt", "-fPIC", "-ccopt", "-O2"}
			for _, f := range a.CFlags {
				argv = append(argv, "-ccopt", f)
			}
			if err := r.run(work, append(argv, s)...); err != nil {
				return err
			}
			objs = append(objs, strings.TrimSuffix(path.Base(s), ".c")+".o")
		}
		lib := "lib" + a.Name + "_stubs.a"
		if err := r.run(work, append([]string{"ar", "rcs", lib}, objs...)...); err != nil {
			return err
		}
		archive = append(archive, "-cclib", "-l"+a.Name+"_stubs")
	}
	for _, l := range a.CLibs {
		archive = append(archive, "-cclib", l)
	}
	c.Phase("archive")
	if err := r.run(work, archive...); err != nil {
		return err
	}
	c.Phase("install")
	return installLibrary(c, work, dest, a)
}

// checkCommon validates the source fields shared by ml-compile and ml-link.
func checkCommon(src, dir string, modules, stubs []string) error {
	if !filepath.IsAbs(src) || !localRel(dir) {
		return builder.Invalidf("source %q / dir %q is not valid", src, dir)
	}
	if err := localFiles(modules); err != nil {
		return err
	}
	return localFiles(stubs)
}

// depOrder returns the module files in dependency order (ocamldep -sort); an interface comes
// right before its implementation.
func depOrder(r *runner, work string, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no .ml or .mli file to compile")
	}
	out, err := r.output(work, append([]string{"ocamlfind", "ocamldep", "-sort"}, files...)...)
	if err != nil {
		return nil, err
	}
	sorted := strings.Fields(out)
	var order []string
	for _, f := range sorted {
		if !slices.Contains(files, f) || slices.Contains(order, f) {
			continue
		}
		if strings.HasSuffix(f, ".ml") && slices.Contains(files, f+"i") && !slices.Contains(order, f+"i") {
			order = append(order, f+"i")
		}
		order = append(order, f)
	}
	for _, f := range files { // ocamldep leaves out nothing it can read; keep the rest in order
		if !slices.Contains(order, f) {
			order = append(order, f)
		}
	}
	return order, nil
}

// output runs argv in dir and returns its standard output (stderr goes to the build log).
func (r *runner) output(dir string, argv ...string) (string, error) {
	argv, err := r.resolve(dir, argv)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := &exec.Cmd{Path: argv[0], Args: argv, Env: r.env, Dir: dir, Stdout: &stdout, Stderr: &stderr}
	err = cmd.Run()
	_, _ = r.log.Write(stderr.Bytes())
	if err != nil {
		r.diags(stderr.Bytes())
		return "", fmt.Errorf("%s: %v", path.Base(argv[0]), err)
	}
	return stdout.String(), nil
}

// moduleBase is the file's module file name without directory and extension ("vec").
func moduleBase(f string) string {
	b := path.Base(f)
	return strings.TrimSuffix(strings.TrimSuffix(b, ".mli"), ".ml")
}

// capital is OCaml's String.capitalize_ascii.
func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// writeAlias writes the alias module <name>__.ml (module Vec = Geom__Vec for every module but
// the main one) and returns its file name.
func writeAlias(work, name string, order []string) (string, error) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, f := range order {
		base := moduleBase(f)
		if base == name || seen[base] {
			continue
		}
		seen[base] = true
		fmt.Fprintf(&b, "module %s = %s__%s\n", capital(base), capital(name), capital(base))
	}
	alias := name + "__.ml"
	return alias, os.WriteFile(filepath.Join(work, alias), []byte(b.String()), 0o644)
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}

// packageFlags is -package p1,p2 (nothing without packages).
func packageFlags(pkgs []string) []string {
	if len(pkgs) == 0 {
		return nil
	}
	return []string{"-package", strings.Join(pkgs, ",")}
}

// installLibrary copies the compiled library and its sources (for editors) to dest and writes
// its META.
func installLibrary(c *builder.Ctx, work, dest string, a CompileArgs) error {
	ents, err := os.ReadDir(work)
	if err != nil {
		return err
	}
	exts := []string{".cmi", ".cmx", ".cmt", ".cmti", ".ml", ".mli"}
	for _, e := range ents {
		n := e.Name()
		keep := slices.Contains(exts, filepath.Ext(n)) || n == a.Name+".cmxa" || n == a.Name+".a" || n == "lib"+a.Name+"_stubs.a"
		if !keep || !e.Type().IsRegular() {
			continue
		}
		if err := copyFile(filepath.Join(work, n), filepath.Join(dest, n)); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dest, "META"), []byte(meta(a.Name, a.Packages)), 0o644)
}

// meta is the findlib META of a local library.
func meta(name string, requires []string) string {
	return fmt.Sprintf("version = \"dev\"\ndescription = \"%s (built by bob)\"\nrequires = %s\narchive(native) = \"%s.cmxa\"\n",
		name, strconv.Quote(strings.Join(requires, " ")), name)
}

// duneLibrary builds the library with a generated dune file (Dune: true) and installs it with
// opam-installer, as an opam package built by dune would be.
func duneLibrary(c *builder.Ctx, r *runner, a CompileArgs, work string) error {
	c.Phase("dune")
	var d strings.Builder
	fmt.Fprintf(&d, "(library\n (name %s)\n (public_name %s)\n (wrapped %t)", a.Name, a.Name, a.Wrapped)
	if len(a.Packages) > 0 {
		fmt.Fprintf(&d, "\n (libraries %s)", strings.Join(a.Packages, " "))
	}
	if len(a.Flags) > 0 {
		fmt.Fprintf(&d, "\n (ocamlopt_flags (:standard %s))", duneAtoms(a.Flags))
	}
	if len(a.Stubs) > 0 {
		var names []string
		for _, s := range a.Stubs {
			names = append(names, strings.TrimSuffix(path.Base(s), ".c"))
		}
		fmt.Fprintf(&d, "\n (foreign_stubs (language c) (names %s) (flags (:standard %s)))", strings.Join(names, " "), duneAtoms(a.CFlags))
	}
	if len(a.CLibs) > 0 {
		fmt.Fprintf(&d, "\n (c_library_flags (%s))", duneAtoms(a.CLibs))
	}
	d.WriteString(")\n")
	files := map[string]string{"dune-project": "(lang dune 3.21)\n", "dune": d.String(), a.Name + ".opam": ""}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(work, n), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := r.run(work, "dune", "build", "-p", a.Name, "-j", strconv.Itoa(cores()), "@install"); err != nil {
		return err
	}
	return r.run(work, "opam-installer", "--prefix="+a.Out, "--libdir="+a.Out+"/lib",
		"--stubsdir="+a.Out+"/lib/stublibs", a.Name+".install")
}

// duneAtoms quotes each argument as a dune string.
func duneAtoms(args []string) string {
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, " ")
}

// linkKind is kind ml-link: one native executable <out>/bin/<name>.
func linkKind(c *builder.Ctx, raw json.RawMessage) error {
	var a LinkArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if a.Name == "" || strings.ContainsAny(a.Name, "/\x00") || !filepath.IsAbs(a.Out) || a.Main == "" {
		return builder.Invalidf("ml-link: name %q / main %q / out %q is invalid", a.Name, a.Main, a.Out)
	}
	if err := checkCommon(a.Src, a.Dir, a.Modules, nil); err != nil {
		return err
	}
	scrub := make([]*regexp.Regexp, len(a.Scrub))
	for i, s := range a.Scrub {
		re, err := regexp.Compile(s)
		if err != nil {
			return builder.Invalidf("ml-link: scrub pattern %q: %v", s, err)
		}
		scrub[i] = re
	}
	r, err := newRunner(c, a.Path, a.Env)
	if err != nil {
		return err
	}
	r.dir = a.Dir
	work, err := copySources(c, a.Src, a.Dir)
	if err != nil {
		return err
	}
	bin := filepath.Join(a.Out, "bin")
	if err := c.MkdirAll(bin); err != nil {
		return err
	}
	prog := filepath.Join(bin, a.Name)
	if a.Dune {
		err = duneExecutable(c, r, a, work, prog)
	} else {
		err = directExecutable(c, r, a, work, prog)
	}
	if err != nil {
		return err
	}
	c.Phase("scrub")
	return scrubFile(prog, c.StoreDir, scrub)
}

func directExecutable(c *builder.Ctx, r *runner, a LinkArgs, work, prog string) error {
	c.Phase("ocamldep")
	order, err := depOrder(r, work, a.Modules)
	if err != nil {
		return err
	}
	// The main module runs last: it is linked after every other module.
	var rest, main []string
	for _, f := range order {
		if moduleBase(f) == a.Main {
			main = append(main, f)
		} else {
			rest = append(rest, f)
		}
	}
	if !slices.Contains(main, a.Main+".ml") {
		return fmt.Errorf("the main module %s.ml is not among the sources", a.Main)
	}
	c.Phase("link")
	argv := append([]string{"ocamlfind", "ocamlopt"}, packageFlags(a.Packages)...)
	argv = append(argv, "-linkpkg", "-g", "-bin-annot")
	argv = append(argv, a.Flags...)
	argv = append(argv, rest...)
	argv = append(argv, main...)
	for _, l := range a.CLibs {
		argv = append(argv, "-cclib", l)
	}
	return r.run(work, append(argv, "-o", prog)...)
}

func duneExecutable(c *builder.Ctx, r *runner, a LinkArgs, work, prog string) error {
	c.Phase("dune")
	var d strings.Builder
	fmt.Fprintf(&d, "(executable\n (name %s)", a.Main)
	if len(a.Packages) > 0 {
		fmt.Fprintf(&d, "\n (libraries %s)", strings.Join(a.Packages, " "))
	}
	if len(a.Flags) > 0 {
		fmt.Fprintf(&d, "\n (ocamlopt_flags (:standard %s))", duneAtoms(a.Flags))
	}
	if len(a.CLibs) > 0 {
		var lf []string
		for _, l := range a.CLibs {
			lf = append(lf, "-cclib", l)
		}
		fmt.Fprintf(&d, "\n (link_flags (%s))", duneAtoms(lf))
	}
	d.WriteString(")\n")
	for n, body := range map[string]string{"dune-project": "(lang dune 3.21)\n", "dune": d.String()} {
		if err := os.WriteFile(filepath.Join(work, n), []byte(body), 0o644); err != nil {
			return err
		}
	}
	exe := a.Main + ".exe"
	if err := r.run(work, "dune", "build", "--root", ".", "--profile", "release", "-j", strconv.Itoa(cores()), "./"+exe); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(work, "_build", "default", exe))
	if err != nil {
		return err
	}
	return os.WriteFile(prog, b, 0o755)
}

// storeRef matches a store path reference: the hash and the name.
var storeRef = regexp.MustCompile(`([0-9a-df-np-sv-z]{32})-([A-Za-z0-9+._?=-]+)`)

// scrubFile replaces the hash of every store path reference in p whose name matches one of
// the patterns with e's (a path that never exists), so the program's runtime closure does not
// contain build-only paths (the compiler's standard library path, the C toolchain).
func scrubFile(p, storeDir string, patterns []*regexp.Regexp) error {
	if len(patterns) == 0 {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	prefix := []byte(storeDir + "/")
	changed := false
	for i := 0; ; {
		j := bytes.Index(b[i:], prefix)
		if j < 0 {
			break
		}
		at := i + j + len(prefix)
		i = at
		m := storeRef.FindSubmatchIndex(b[at:min(len(b), at+256)])
		if m == nil || m[0] != 0 {
			continue
		}
		name := string(b[at+m[4] : at+m[5]])
		for _, re := range patterns {
			if re.MatchString(name) {
				copy(b[at:at+32], bytes.Repeat([]byte("e"), 32))
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if err := os.Chmod(p, info.Mode().Perm()|0o200); err != nil {
		return err
	}
	return os.WriteFile(p, b, info.Mode().Perm())
}
