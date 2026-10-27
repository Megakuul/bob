package core

// ADR 0020 guards (owner decisions D71/D72, 2026-10-24): the typed public surface (R-FW-072),
// the generated typed nixpkgs surface (R-NIX-031), package locality (R-FW-070), one build unit
// per package (R-FW-071, R-CC-006) and typed slots (R-FW-071). HARNESS.md §18.
//
// The allowlist below is protected: a new raw-string parameter or field of the public API fails
// CI until it is classified here, which only an owner-visible change of this file can do.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/harness/boblint"
	"github.com/megakuul/bob/tests/parity"
)

// typedSurfacePackages are the build-definition API packages (SPEC R-ARCH-003 L0–L3) whose
// exported parameters and fields R-FW-072 checks. pkg/bob/ext is left out: it is the extension
// API for ecosystem authors (R-FW-046), whose kind names and directories are its subject.
var typedSurfacePackages = []string{
	"pkg/bob", "pkg/nixpkgs", "pkg/nixpkgs/nixext", "pkg/cc", "pkg/golang", "pkg/rust", "pkg/npm",
	"pkg/ocaml", "pkg/proto", "pkg/connect", "pkg/graphql", "pkg/openapi", "pkg/services/postgres",
	"pkg/output/oci", "pkg/output/appimage", "pkg/output/tarball", "pkg/release",
}

// Categories of R-FW-072 (SPEC §1.4.5): what a raw string may carry.
const (
	ownName      = "own-name"             // the label name of the package being defined, or a name in its own namespace
	localPath    = "local-path"           // a path inside the calling package's region (R-FW-070)
	outputPath   = "output-path"          // a path inside the package's own output
	version      = "version"              // a version, version requirement or git revision
	hash         = "hash"                 // a content hash or revision id
	url          = "url"                  // a download URL
	glob         = "glob"                 // a glob pattern over own files, branches or tags
	command      = "command"              // argv of a program
	env          = "env"                  // environment variable names and values
	flag         = "flag"                 // compiler/linker flags, defines, build tags
	text         = "text"                 // free text (descriptions, CVE ids, hostnames, OCI labels)
	labelPattern = "label-pattern"        // a CLI label or pattern (R-FW-015)
	registryDecl = "registry-declaration" // the one place an external registry name is written
	resolvedNS   = "resolved-namespace"   // a name inside a resolved package, validated at lock/plan
	targetLang   = "target-language"      // an identifier of the generated code's language
	value        = "value"                // a field of a value bob returns
	extension    = "extension"            // filled by bob's own packages, never by users
)

// typedSurfaceAllowed classifies every raw-string parameter and field the public API may have.
var typedSurfaceAllowed = map[string]string{
	"pkg/bob.(*Step).File(p)":                 outputPath,
	"pkg/bob.CmdReady(argv)":                  command,
	"pkg/bob.DevShell.Env":                    env,
	"pkg/bob.DevShell.Hooks":                  command,
	"pkg/bob.DevShell.Name":                   ownName,
	"pkg/bob.Dir(p)":                          localPath,
	"pkg/bob.Exe(mainProgram)":                outputPath,
	"pkg/bob.FetchOpts.Mirrors":               url,
	"pkg/bob.FetchOpts.Name":                  ownName,
	"pkg/bob.FetchOpts.URL":                   url,
	"pkg/bob.Files(paths)":                    localPath,
	"pkg/bob.Glob(patterns)":                  glob,
	"pkg/bob.Import(module)":                  registryDecl,
	"pkg/bob.Import(version)":                 version,
	"pkg/bob.Label.Dir":                       value,
	"pkg/bob.Label.Module":                    value,
	"pkg/bob.Label.Name":                      value,
	"pkg/bob.Label.Nix":                       value,
	"pkg/bob.LinkInfo.Archives":               value,
	"pkg/bob.LinkInfo.IncludeDirs":            value,
	"pkg/bob.LinkInfo.Libs":                   value,
	"pkg/bob.LinkInfo.LinkArgs":               value,
	"pkg/bob.LinkInfo.LinkDirs":               value,
	"pkg/bob.LinkInfo.PkgConfig":              value,
	"pkg/bob.LinkInfo.RuntimeDirs":            value,
	"pkg/bob.NamedVariant(name)":              ownName,
	"pkg/bob.OnPush(branchGlob)":              glob,
	"pkg/bob.OnTag(glob)":                     glob,
	"pkg/bob.Output.Name":                     value,
	"pkg/bob.Parse(s)":                        labelPattern,
	"pkg/bob.ParseConfigured(named)":          labelPattern,
	"pkg/bob.ParseConfigured(s)":              labelPattern,
	"pkg/bob.ParseLabel(s)":                   labelPattern,
	"pkg/bob.Pipeline(name)":                  ownName,
	"pkg/bob.Position.File":                   value,
	"pkg/bob.Run(name)":                       ownName,
	"pkg/bob.RunOpts.Cmd":                     command,
	"pkg/bob.RunOpts.Env":                     env,
	"pkg/bob.RunOpts.Inputs":                  env,
	"pkg/bob.RunOpts.Outputs":                 outputPath,
	"pkg/bob.ServiceOptions.Args":             command,
	"pkg/bob.ServiceOptions.Env":              env,
	"pkg/bob.ServiceSpec.Env":                 env,
	"pkg/bob.ServiceSpec.Name":                ownName,
	"pkg/bob.ServiceSpec.Start":               command,
	"pkg/bob.Toolchain.EmulatorCmd":           outputPath,
	"pkg/bob.Toolchain.Prefix":                value,
	"pkg/bob.Toolchain.Triple":                value,
	"pkg/bob.Variant.Named":                   value,
	"pkg/bob.WithFeatures(features)":          resolvedNS,
	"pkg/cc.BinaryOpts.Defines":               flag,
	"pkg/cc.BinaryOpts.Flags":                 flag,
	"pkg/cc.BinaryOpts.LinkOpts":              flag,
	"pkg/cc.External(name)":                   ownName,
	"pkg/cc.ExternalOpts.Args":                flag,
	"pkg/cc.ExternalOpts.Include":             outputPath,
	"pkg/cc.ExternalOpts.Libs":                ownName, // the libraries the External itself installs
	"pkg/cc.ExternalOpts.URL":                 url,
	"pkg/cc.LibraryOpts.Defines":              flag,
	"pkg/cc.LibraryOpts.Flags":                flag,
	"pkg/cc.LibraryOpts.LinkOpts":             flag,
	"pkg/cc.NewBinary(name)":                  ownName,
	"pkg/cc.NewTest(name)":                    ownName,
	"pkg/cc.SharedLibrary(name)":              ownName,
	"pkg/cc.StaticLibrary(name)":              ownName,
	"pkg/connect.GoOpts.Dir":                  localPath,
	"pkg/connect.TSOpts.Dir":                  localPath,
	"pkg/golang.BinaryOpts.Name":              ownName,
	"pkg/golang.BinaryOpts.Tags":              flag,
	"pkg/golang.BinaryOpts.X":                 flag, // -X symbols of the package's own code
	"pkg/golang.Generated.Dir":                localPath,
	"pkg/golang.Generated.Kind":               extension,
	"pkg/golang.GeneratorRuntime(path)":       registryDecl,
	"pkg/golang.ModuleOpts.Go":                version,
	"pkg/golang.ModuleOpts.Path":              ownName,
	"pkg/golang.ModuleOpts.Tags":              flag,
	"pkg/golang.Require(path)":                registryDecl,
	"pkg/golang.Require(version)":             version,
	"pkg/golang.Requirement.Path":             registryDecl,
	"pkg/golang.Requirement.Version":          version,
	"pkg/golang.TestOpts.Args":                command,
	"pkg/golang.TestOpts.Name":                ownName,
	"pkg/golang.TestOpts.Tags":                flag,
	"pkg/graphql.GqlgenOpts.Autobind":         targetLang,
	"pkg/graphql.GqlgenOpts.Dir":              localPath,
	"pkg/graphql.GqlgenOpts.Models":           targetLang,
	"pkg/graphql.TSOpts.Dir":                  localPath,
	"pkg/graphql.TSOpts.Scalars":              targetLang,
	"pkg/nixpkgs.AllowInsecure(ids)":          text,
	"pkg/nixpkgs.Modules(names)":              resolvedNS,
	"pkg/npm.Alias(as)":                       ownName,
	"pkg/npm.Alias(name)":                     registryDecl,
	"pkg/npm.Alias(req)":                      version,
	"pkg/npm.AppOpts.Entry":                   localPath,
	"pkg/npm.AppOpts.Name":                    ownName,
	"pkg/npm.Dep(name)":                       registryDecl,
	"pkg/npm.Dep(req)":                        version,
	"pkg/npm.DevServer.Host":                  text,
	"pkg/npm.GenRuntime.Name":                 registryDecl,
	"pkg/npm.Generated.Dir":                   localPath,
	"pkg/npm.Generated.Kind":                  extension,
	"pkg/npm.LibOpts.Entry":                   localPath,
	"pkg/npm.LibOpts.Name":                    ownName,
	"pkg/npm.Quirk.Env":                       env,
	"pkg/npm.Quirk.Package":                   registryDecl,
	"pkg/npm.Quirk.Range":                     version,
	"pkg/npm.Quirk.Test":                      command,
	"pkg/npm.Quirk.Why":                       text,
	"pkg/npm.TSConfig.Exclude":                glob,
	"pkg/npm.TSConfig.Extends":                localPath,
	"pkg/npm.TSConfig.Extra":                  flag,
	"pkg/npm.TSConfig.Include":                glob,
	"pkg/npm.TSConfig.Lib":                    targetLang,
	"pkg/npm.TSConfig.Types":                  targetLang,
	"pkg/npm.ViteOpts.Config":                 localPath,
	"pkg/npm.ViteOpts.Name":                   ownName,
	"pkg/ocaml.(*OpamPkg).Lib(findlib)":       resolvedNS,
	"pkg/ocaml.ExeOpts.Main":                  ownName, // a module of the package's own code
	"pkg/ocaml.ExeOpts.Name":                  ownName,
	"pkg/ocaml.LibOpts.Flags":                 flag,
	"pkg/ocaml.LibOpts.Name":                  ownName,
	"pkg/ocaml.Opam(name)":                    registryDecl,
	"pkg/ocaml.Opam(req)":                     version,
	"pkg/openapi.GoOpts.Dir":                  localPath,
	"pkg/openapi.GoOpts.Package":              ownName,
	"pkg/openapi.NewSpec(file)":               localPath,
	"pkg/openapi.TSOpts.Dir":                  localPath,
	"pkg/output/appimage.BundleOpts.Entries":  ownName,
	"pkg/output/appimage.BundleOpts.Env":      env,
	"pkg/output/appimage.BundleOpts.Locales":  text,
	"pkg/output/appimage.BundleOpts.Prune":    glob,
	"pkg/output/appimage.Desktop.Categories":  text,
	"pkg/output/appimage.Desktop.Name":        text,
	"pkg/output/appimage.NewBundle(name)":     ownName,
	"pkg/output/oci.ImageOpts.Cmd":            command,
	"pkg/output/oci.ImageOpts.Entrypoint":     command,
	"pkg/output/oci.ImageOpts.Env":            env,
	"pkg/output/oci.ImageOpts.Labels":         text,
	"pkg/output/oci.ImageOpts.Prune":          glob,
	"pkg/output/oci.ImageOpts.User":           text,
	"pkg/output/oci.ImageOpts.WorkDir":        outputPath,
	"pkg/output/oci.NewImage(name)":           ownName,
	"pkg/output/tarball.New(name)":            ownName,
	"pkg/proto.Breaking(againstRev)":          version,
	"pkg/release.New(name)":                   ownName,
	"pkg/rust.(*Lib).With(features)":          resolvedNS,
	"pkg/rust.BinOpts.BuildScript":            localPath,
	"pkg/rust.BinOpts.Default":                ownName, // features of the crate's own table
	"pkg/rust.BinOpts.Enable":                 ownName,
	"pkg/rust.BinOpts.Features":               ownName,
	"pkg/rust.BinOpts.Name":                   ownName,
	"pkg/rust.BinOpts.Root":                   localPath,
	"pkg/rust.Crate(name)":                    registryDecl,
	"pkg/rust.Crate(req)":                     version,
	"pkg/rust.Features(fs)":                   resolvedNS,
	"pkg/rust.LibOpts.BuildScript":            localPath,
	"pkg/rust.LibOpts.Default":                ownName,
	"pkg/rust.LibOpts.Features":               ownName,
	"pkg/rust.LibOpts.Name":                   ownName,
	"pkg/rust.LibOpts.Root":                   localPath,
	"pkg/rust.Rename(codeName)":               ownName,
	"pkg/rust.TestOpts.Args":                  command,
	"pkg/services/postgres.Options.Databases": ownName,
}

// typedSurfaceNew classifies the raw-string parameters the ADR 0020 API adds (Q2/Q3). They may
// be absent until the units land; once present they are checked like the rows above.
var typedSurfaceNew = map[string]string{
	"pkg/bob.IncludeDir(dir)":                  localPath,
	"pkg/bob.RepoOpts.NixpkgsRev":              hash,
	"pkg/nixpkgs.MainProgram(path)":            outputPath,
	"pkg/nixpkgs/nixext.New(name)":             extension, // bob.go cannot import nixext (R-FW-002 R7)
	"pkg/nixpkgs/nixext.MainProgram(path)":     outputPath,
	"pkg/nixpkgs/nixext.Modules(names)":        resolvedNS,
	"pkg/nixpkgs/nixext.AllowInsecure(ids)":    text,
	"pkg/nixpkgs/nixext.Toolchain.EmulatorCmd": outputPath,
	"pkg/rust.Indirect(name)":                  registryDecl,
	"pkg/npm.Indirect(name)":                   registryDecl,
	"pkg/ocaml.Indirect(name)":                 registryDecl,
}

// typedSurfaceViolations are the stringly-typed references of docs/review/typesafety-audit.md
// that Q2/Q3 replace by typed forms. While one exists the test is red (not implemented yet);
// they must never be moved into the allowlists.
var typedSurfaceViolations = map[string]string{
	"pkg/bob.PipelineOpts.Releases":         "V07 release names → []bob.Pattern",
	"pkg/bob.RepoOpts.Nixpkgs":              "V06 channel name → bob.Channel",
	"pkg/bob.Toolchain.Bintools":            "V05 nixpkgs attribute → nixext.Toolchain(p).Bintools",
	"pkg/bob.Toolchain.CC":                  "V05 nixpkgs attribute → nixext.Toolchain(p).CC",
	"pkg/bob.Toolchain.CCLib":               "V05 nixpkgs attribute → nixext.Toolchain(p).CCLib",
	"pkg/bob.Toolchain.Emulator":            "V05 nixpkgs attribute → nixext.Toolchain(p).Emulator",
	"pkg/cc.LibraryOpts.Hdrs":               "V21 include dir string → bob.Include",
	"pkg/golang.(*Module).Binary(dir)":      "V09 directory of another package → caller's directory",
	"pkg/golang.(*Module).CArchive(dir)":    "V09 directory of another package → caller's directory",
	"pkg/golang.(*Module).CShared(dir)":     "V09 directory of another package → caller's directory",
	"pkg/golang.(*Module).Cgo(dir)":         "V09 directory of another package → caller's directory",
	"pkg/golang.(*Module).Tests(pattern)":   "V09 package pattern → TestOpts.Recursive",
	"pkg/golang.GenLabel(dir)":              "V10 internal helper in the public API → internal package",
	"pkg/golang.Generated.Tools":            "V11 nixpkgs attributes → []nixpkgs.Attr",
	"pkg/golang.GeneratorRuntime(attr)":     "V12 nixpkgs attribute → nixpkgs.Attr",
	"pkg/nixpkgs.Exe(attr)":                 "V01 nixpkgs attribute → nixpkgs.<Name>.Exe(p)",
	"pkg/nixpkgs.Exe(mainProgram)":          "V02 → meta.mainProgram / nixpkgs.MainProgram option",
	"pkg/nixpkgs.Lib(attr)":                 "V01 nixpkgs attribute → nixpkgs.<Name>.Lib(p)",
	"pkg/nixpkgs.Pkg(attr)":                 "V01 nixpkgs attribute → nixpkgs.<Name>.Pkg(p)",
	"pkg/nixpkgs.Tool(attr)":                "V01 nixpkgs attribute → nixpkgs.<Name>.Tool(p)",
	"pkg/nixpkgs.Outputs(names)":            "V03 output names → nixpkgs.Output constants",
	"pkg/npm.AllowScripts(pkgs)":            "V15 npm names → *npm.Pkg handles",
	"pkg/npm.Extend(pkg)":                   "V15 npm name → *npm.Pkg handle",
	"pkg/npm.Peer(name)":                    "V15 npm name → *npm.Pkg handle",
	"pkg/npm.GenRuntime.Generator":          "V13 nixpkgs attribute → nixpkgs.Attr",
	"pkg/npm.Generated.Tools":               "V11 nixpkgs attributes → []nixpkgs.Attr",
	"pkg/npm.Quirk.Tools":                   "V14 nixpkgs attributes → []nixpkgs.Attr",
	"pkg/npm.TSConfig.JSX":                  "V31 closed set → npm.JSX constants",
	"pkg/npm.TSConfig.Module":               "V31 closed set → npm.ModuleKind constants",
	"pkg/npm.TSConfig.ModuleResolution":     "V31 closed set → npm.Resolution constants",
	"pkg/npm.TSConfig.Target":               "V31 closed set → npm.ESTarget constants",
	"pkg/ocaml.Conf(opamName)":              "V18 opam name → *ocaml.OpamPkg handle",
	"pkg/proto.LibraryOpts.Root":            "V25 directory outside the package → derived import root",
	"pkg/rust.CfgTable(triple)":             "V20 internal helper in the public API → internal package",
	"pkg/rust.LibOpts.Headers":              "V21 include dir string → bob.Include",
	"pkg/rust.Uses(crate)":                  "V16 crate name → *rust.CrateDep handle",
	"pkg/services/postgres.Options.Version": "V19 nixpkgs attribute suffix → Server nixpkgs.Attr",
}

// R-FW-072: every raw-string parameter and field of the public API is classified; the audited
// violations are reported red until they are gone; anything else fails.
func TestRFW072TypedSurface(t *testing.T) {
	parity.Covers(t, "R-FW-072", "R-FW-010")
	root := harness.Root(t)
	var dirs []string
	for _, d := range typedSurfacePackages {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d))); err != nil {
			if d == "pkg/nixpkgs/nixext" {
				continue // created by Q2 (R-NIX-031); its absence is reported by TestRNIX031GeneratedSurface
			}
			t.Fatalf("harness: %s: %v", d, err)
		}
		dirs = append(dirs, d)
	}
	found, err := boblint.APIStrings(root, dirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 50 {
		t.Fatalf("harness: only %d raw-string parameters found; the walker is broken", len(found))
	}
	seen := map[string]bool{}
	var red []string
	for _, f := range found {
		seen[f.Key] = true
		switch {
		case typedSurfaceAllowed[f.Key] != "", typedSurfaceNew[f.Key] != "":
		case typedSurfaceViolations[f.Key] != "":
			red = append(red, f.Key+" ("+typedSurfaceViolations[f.Key]+", "+f.Pos+")")
		default:
			t.Errorf("%s: %s takes a raw string; name the package/attribute/target by a typed value, or classify the parameter in tests/core/typesafe_test.go (owner-protected, R-FW-072)", f.Pos, f.Key)
		}
	}
	for k := range typedSurfaceAllowed {
		if !seen[k] {
			t.Errorf("stale allowlist row %q: the parameter no longer exists; remove the row", k)
		}
	}
	for k := range typedSurfaceViolations {
		if typedSurfaceAllowed[k] != "" || typedSurfaceNew[k] != "" {
			t.Errorf("harness: %s is both a violation and allowed", k)
		}
	}
	if len(red) > 0 {
		sort.Strings(red)
		harness.RedFeature(t, "typed surface (ADR 0020): stringly-typed references remain:\n  "+strings.Join(red, "\n  "))
	}
}

// requiredHandles must exist in the generated surface (R-NIX-031 acceptance); excludedHandles
// must not (language package sets and scopes, ADR 0020 §2).
var (
	requiredHandles = []string{"Openssl", "Zlib", "Protobuf", "Jq", "Tzdata", "Curl", "Bash", "Coreutils",
		"Patchelf", "Gcc", "Go1_26", "Rustc", "Nodejs", "Postgresql", "Postgresql17"}
	excludedHandles = []string{"Python3Packages", "PythonPackages", "OcamlPackages", "OcamlNg", "HaskellPackages",
		"PerlPackages", "NodePackages", "RubyPackages", "LuaPackages", "RPackages", "EmacsPackages", "VimPlugins",
		"PkgsCross", "PkgsStatic", "PkgsMusl", "LinuxPackages", "Lib", "Darwin"}
	generatedDoc = regexp.MustCompile(`^// [A-Z][A-Za-z0-9_]* is \S+ \S+: .*Licence: [^\n]+`)
)

// R-NIX-031: pkg/nixpkgs/attrs_gen.go is the generated typed surface (header, go:generate,
// handles, doc comments, exclusions), and pkg/nixpkgs has no string role functions.
func TestRNIX031GeneratedSurface(t *testing.T) {
	parity.Covers(t, "R-NIX-031", "R-FW-032")
	root := harness.Root(t)
	gen := filepath.Join(root, "pkg", "nixpkgs", "attrs_gen.go")
	src, err := os.ReadFile(gen)
	if os.IsNotExist(err) {
		harness.RedFeature(t, "generated typed nixpkgs surface pkg/nixpkgs/attrs_gen.go (R-NIX-031, unit Q2)")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^// Code generated by nixpkgsgen from nixpkgs [0-9a-f]{40}\. DO NOT EDIT\.$`).Match(src) {
		t.Errorf("attrs_gen.go lacks the header \"// Code generated by nixpkgsgen from nixpkgs <rev>. DO NOT EDIT.\"")
	}
	directive := false
	ents, _ := os.ReadDir(filepath.Join(root, "pkg", "nixpkgs"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".go") && e.Name() != "attrs_gen.go" {
			b, _ := os.ReadFile(filepath.Join(root, "pkg", "nixpkgs", e.Name()))
			if regexp.MustCompile(`(?m)^//go:generate go run [^\n]*cmd/nixpkgsgen`).Match(b) {
				directive = true
			}
		}
	}
	if !directive {
		t.Errorf("pkg/nixpkgs has no //go:generate directive running cmd/nixpkgsgen")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, gen, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("attrs_gen.go: %v", err)
	}
	handles := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			doc := vs.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			for _, n := range vs.Names {
				if !n.IsExported() {
					continue
				}
				handles[n.Name] = true
				if doc == nil || !generatedDoc.MatchString(strings.TrimSpace("// "+doc.Text())) || !strings.HasPrefix(doc.Text(), n.Name+" is ") {
					t.Errorf("%s: handle %s has no doc comment \"%s is <attr> <version>: <description>. Licence: <spdx>.\"", fset.Position(n.Pos()), n.Name, n.Name)
				}
			}
		}
	}
	if len(handles) < 10000 {
		t.Errorf("attrs_gen.go defines %d handles; the pin has ~27 000 top-level names, so the surface is incomplete", len(handles))
	}
	for _, h := range requiredHandles {
		if !handles[h] {
			t.Errorf("the generated surface has no handle nixpkgs.%s", h)
		}
	}
	for _, h := range excludedHandles {
		if handles[h] {
			t.Errorf("the generated surface has nixpkgs.%s, an excluded package set (ADR 0020 §2)", h)
		}
	}
	for _, fn := range []string{"Tool", "Lib", "Pkg", "Exe"} {
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") || e.Name() == "attrs_gen.go" {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(root, "pkg", "nixpkgs", e.Name()))
			if regexp.MustCompile(`(?m)^func ` + fn + `\(p \*bob\.P, attr string`).Match(b) {
				t.Errorf("pkg/nixpkgs/%s still defines the string role function %s(p, attr string) (R-FW-014: one way)", e.Name(), fn)
			}
		}
	}
	// The generator's own offline check: byte-identical regeneration from the snapshot, the
	// snapshot revision equals lock.DefaultRev, every handle is in the pinned index.
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go binary on PATH")
	}
	r := harness.RunPath(t, goBin, harness.Spec{Dir: root, Timeout: 10 * time.Minute,
		Env: []string{"GOFLAGS=", "GOWORK=off", "GOPROXY=off", "GOCACHE=" + os.Getenv("GOCACHE"), "GOMODCACHE=" + os.Getenv("GOMODCACHE"), "GOPATH=" + os.Getenv("GOPATH")}},
		"run", "./cmd/nixpkgsgen", "-check")
	if r.Exit != 0 {
		t.Errorf("go run ./cmd/nixpkgsgen -check: exit %d\n%s", r.Exit, r.Combined())
	}
}

// lintRoots are the repositories whose bob.go files R-FW-070/071 lint: every fixture and every
// test repository. Repositories that are faults by design are listed with their reason.
var lintExempt = map[string]string{
	"tests/core/testdata/repos/srcerr":    "R-FW-021 error fixtures: sources outside the repository on purpose",
	"tests/core/testdata/repos/rules":     "R-FW-002 rule fixtures: rule violations on purpose",
	"tests/core/testdata/repos/typecheck": "R-FW-010/032 type-error fixtures",
	"tests/fixtures/evil":                 "R-FW-045 hostile bob.go fixtures",
}

// R-FW-070, R-FW-071, R-CC-006, R-NIX-031: no fixture or test repository lists sources outside
// its package, reaches into a subpackage, names a Go package of another directory or a nixpkgs
// package by string, imports nixext, or defines two build units in one package.
func TestRFW070FixtureLocality(t *testing.T) {
	parity.Covers(t, "R-FW-070", "R-FW-071", "R-CC-006")
	root := harness.Root(t)
	var repos []string
	for _, base := range []string{"tests/fixtures/repos", "tests/core/testdata/repos", "tests/cli/testdata/repos", "tests/devenv/testdata/repos"} {
		filepath.WalkDir(filepath.Join(root, base), func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			for ex := range lintExempt {
				if rel == ex || strings.HasPrefix(rel, ex+"/") {
					return filepath.SkipDir
				}
			}
			if _, err := os.Stat(filepath.Join(p, "bob.lock")); err == nil {
				repos = append(repos, p)
			}
			return nil
		})
	}
	if len(repos) < 20 {
		t.Fatalf("harness: found only %d repositories to lint", len(repos))
	}
	for _, dir := range repos {
		r, err := boblint.LoadRepo(dir)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, dir)
		for _, p := range boblint.Check(r) {
			t.Errorf("%s/%s", filepath.ToSlash(rel), p)
		}
	}
}

// The linter itself catches each violation class (harness self-check, HARNESS.md §10.3).
func TestBoblintCatchesViolations(t *testing.T) {
	cases := []struct {
		name, rule string
		repo       boblint.Repo
	}{
		{"dotdot", "R-FW-070", boblint.Repo{"app/bob.go": bobgo("app", `cc.NewBinary(p, "app", cc.BinaryOpts{Srcs: bob.Glob("../lib/*.c")})`), "lib/bob.go": bobgo("lib", "")}},
		{"subpackage", "R-FW-070", boblint.Repo{"bob.go": bobgo("root", `cc.NewBinary(p, "all", cc.BinaryOpts{Srcs: bob.Glob("lib/src/*.c")})`), "lib/bob.go": bobgo("lib", "")}},
		{"include-outside", "R-FW-070", boblint.Repo{"a/bob.go": bobgo("a", `cc.StaticLibrary(p, "a", cc.LibraryOpts{Srcs: bob.Glob("*.c"), Hdrs: bob.IncludeDir("../b/include")})`)}},
		{"absolute", "R-FW-070", boblint.Repo{"a/bob.go": bobgo("a", `cc.NewBinary(p, "a", cc.BinaryOpts{Srcs: bob.Files("/etc/x.c")})`)}},
		{"go-dir", "R-FW-070", boblint.Repo{"bob.go": bobgo("root", `Module(p).Binary("./cmd/api", golang.BinaryOpts{})`)}},
		{"nix-string", "R-NIX-031", boblint.Repo{"bob.go": bobgo("root", `nixpkgs.Lib(p, "openssl")`)}},
		{"two-units", "R-CC-006", boblint.Repo{"a/bob.go": bobgo("a", `cc.StaticLibrary(p, "a", cc.LibraryOpts{}), cc.NewBinary(p, "b", cc.BinaryOpts{})`)}},
		{"nixext", "R-NIX-031", boblint.Repo{"a/bob.go": "//go:build bob\n\npackage a\n\nimport \"github.com/megakuul/bob/pkg/nixpkgs/nixext\"\n\nvar X = nixext.New(\"gcc\")\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := boblint.Check(c.repo)
			for _, p := range ps {
				if p.Rule == c.rule {
					return
				}
			}
			t.Errorf("boblint did not report %s: %v", c.rule, ps)
		})
	}
	ok := boblint.Repo{
		"bob.go":     bobgo("root", `cc.StaticLibrary(p, "r", cc.LibraryOpts{Srcs: bob.Glob("src/**/*.c"), Hdrs: bob.IncludeDir("include")}), cc.NewTest(p, "t", cc.BinaryOpts{Srcs: bob.Files("test/t.c")})`),
		"lib/bob.go": bobgo("lib", `nixpkgs.Openssl.Lib(p), Module(p).Binary(golang.BinaryOpts{})`),
	}
	if ps := boblint.Check(ok); len(ps) > 0 {
		t.Errorf("boblint reports a valid repository: %v", ps)
	}
}

func bobgo(pkg, body string) string {
	return "//go:build bob\n\npackage " + pkg + "\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/cc\"\n\t\"github.com/megakuul/bob/pkg/golang\"\n\t\"github.com/megakuul/bob/pkg/nixpkgs\"\n)\n\nvar X = bob.Define(func(p *bob.P) any { return []any{" + body + "} })\n"
}

// R-FW-070 (b): "**" in the root bob.go never descends into a subpackage: changing a file of
// libs/mathx (own bob.go) leaves the root step's sources unchanged, changing a file of a plain
// subdirectory changes them.
func TestRFW070GlobStopsAtSubpackage(t *testing.T) {
	parity.Covers(t, "R-FW-070")
	dir := harness.Fixture(t, "cc")
	bobgoPath := filepath.Join(dir, "bob.go")
	writeFile(t, bobgoPath, readFile(t, bobgoPath)+`
// Region reads every .c file of the root package's region (test of R-FW-070).
var Region = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "region", bob.RunOpts{Srcs: bob.Glob("**/*.c"), Cmd: []string{"true"}, Outputs: []string{"x"}})
})
`)
	if err := os.MkdirAll(filepath.Join(dir, "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "extra", "x.c"), "int x;\n")
	git(t, dir, "add", "-A")
	git(t, dir, "-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "region")
	g, ok := evalIR(t, dir)
	if !ok {
		return
	}
	id := ""
	for _, n := range g.Nodes {
		if n.Label == "//:region" {
			id = n.ID
		}
	}
	if id == "" {
		t.Fatalf("no node //:region in the IR")
	}
	before, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	mathx := filepath.Join(dir, "libs", "mathx", "src", "mathx.c")
	writeFile(t, mathx, readFile(t, mathx)+"\n/* changed */\n")
	after, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	if before[id] != after[id] {
		t.Errorf("changing libs/mathx/src/mathx.c (a subpackage with its own bob.go) changed the sources of //:region")
	}
	writeFile(t, filepath.Join(dir, "extra", "x.c"), "int x = 1;\n")
	again, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	if again[id] == after[id] {
		t.Errorf("changing extra/x.c (in the root package's region) did not change the sources of //:region")
	}
}

// R-FW-071: typed slots (repos define/slot, define/slot2).
func TestRFW071Slots(t *testing.T) {
	parity.Covers(t, "R-FW-071")
	dir := repo(t, "define/slot")
	g, ok := evalIR(t, dir, "//use:use")
	if !ok {
		return
	}
	var use, prog string
	for _, n := range g.Nodes {
		switch n.Label {
		case "//use:use":
			use = n.ID
		case "//leaf:prog":
			prog = n.ID
			if !strings.HasPrefix(n.DefinedAt.File, "leaf/bob.go") {
				t.Errorf("//leaf:prog is defined at %s, want leaf/bob.go (the bound constructor)", n.DefinedAt)
			}
		}
	}
	if use == "" || prog == "" {
		t.Fatalf("IR lacks //use:use or //leaf:prog (slot not resolved)")
	}
	for _, n := range g.Nodes {
		if n.ID == use && !containsString(n.Deps, prog) {
			t.Errorf("//use:use does not depend on //leaf:prog through the slot: deps %v", n.Deps)
		}
	}
	r := bobRun(t, dir, "build", "//bad:bad", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	if e, ok := errorWithCode(t, r, "BOB-E-1020"); ok && !strings.Contains(e.Message, "is not bound; bind it with") {
		t.Errorf("unbound slot message: %q", e.Message)
	}
	r = bobRun(t, repo(t, "define/slot2"), "build", "//...", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	if e, ok := errorWithCode(t, r, "BOB-E-1020"); ok && !strings.Contains(e.Message, "is already bound at") {
		t.Errorf("twice-bound slot message: %q", e.Message)
	}
}

func containsString(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
