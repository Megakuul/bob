---
title: API index
section: Reference
order: 2
summary: Every exported function, type and method of the bob.go packages, generated from the source.
---
This page lists every exported function, type and method of the packages a `bob.go` file
may import. It is generated from the source code, so it always matches the framework of the
same bob version. For explanations and examples, read the
[bob.go reference](../04-guides/01-bob-go-reference.md).

> [!NOTE]
> Types are listed with the first sentence of their documentation. Struct fields and option
> details are in the doc comments: run `go doc github.com/megakuul/bob/pkg/golang` (or
> any other package) in a checkout of bob, or hover in your editor.

## bob

`import "github.com/megakuul/bob/pkg/bob"`

Functions:

- `func CmdReady(argv ...string) Probe`
- `func Define[T any](fn func(p *P) T) func(p *P) T`
- `func Dir(p string) Sources`
- `func Exe(t Tree, mainProgram string) Executable`
- `func Fetch(p *P, o FetchOpts) Tree`
- `func Files(paths ...string) Sources`
- `func Glob(patterns ...string) Sources`
- `func Import(module, version string) struct{}`
- `func Main()`
- `func NamedVariant(name string, mods ...VariantMod) Named`
- `func NewShell(p *P, s DevShell) *Shell`
- `func OnPullRequest() Trigger`
- `func OnPush(branchGlob string) Trigger`
- `func OnTag(glob string) Trigger`
- `func Parse(s string) (Ref, error)`
- `func ParseConfigured(s string, host Platform, named ...string) (Configured, error)`
- `func ParseLabel(s string) (Label, error)`
- `func Pipeline(name string, o PipelineOpts) struct{}`
- `func Repo(o RepoOpts) struct{}`
- `func Run(p *P, name string, o RunOpts) *Step`
- `func ServiceFrom(bin Tool, o ServiceOptions) Service`
- `func SpecOf(s Service) *ServiceSpec`
- `func TCPReady(port int) Probe`
- `func ToolchainFor(host, target Platform) Toolchain`
- `func WithFeatures(crate Package, features ...string) VariantMod`
- `func WithProfile(p Profile) VariantMod`
- `func WithRace() VariantMod`
- `func WithSanitize(s ...Sanitizer) VariantMod`

Types and methods:

- `bob.Concurrency`: Concurrency says what happens to a run of the same pipeline and ref while one is active.
- `bob.Configured`: Configured is a configured label: a label, a platform and a variant (R-FW-050).
  - `func (c Configured) String() string`
- `bob.DevShell`: DevShell declares one development environment for bob dev, bob env, bob exec and the shell hook (SPEC §02.7.0).
- `bob.Executable`: Executable is a package with one main program, e.g.
- `bob.FetchOpts`: FetchOpts declares one external file or archive.
- `bob.Label`: Label identifies a package, e.g.
  - `func (l Label) String() string`
- `bob.LinkInfo`: LinkInfo is what a Linkable gives its consumers (cgo, Rust Native, OCaml Native, cc Deps).
- `bob.Linkable`: Linkable is a native (C ABI) library.
- `bob.Named`: Named is a reusable variant referred to by name from the CLI and pipelines.
  - `func (n Named) Name() string`
- `bob.Node`: Node is embedded by every ecosystem package type.
  - `func (n *Node) DefinedAt() Position`
  - `func (n *Node) Label() Label`
  - `func (n *Node) Outputs() []Output`
- `bob.Output`: Output names one store output of a package, e.g.
- `bob.P`: P is the configuration a constructor builds for: one platform and one variant.
  - `func (p *P) Host() *P`
  - `func (p *P) Platform() Platform`
  - `func (p *P) Toolchain() Toolchain`
  - `func (p *P) Variant() Variant`
- `bob.Package`: Package is a node of the build graph.
- `bob.Pattern`: Pattern is a label pattern ("//...", "//dir/...", "//dir:all", a label, "//dir"); a string type so that fixtures can write []bob.Pattern{"//..."}.
  - `func (p Pattern) Match(l Label) bool`
  - `func (p Pattern) String() string`
  - `func (p Pattern) Valid() error`
- `bob.PipelineOpts`: PipelineOpts is typed; the zero value is valid and means a manual-only pipeline.
- `bob.Platform`: Platform is an operating system and CPU architecture packages are built for.
  - `func (p Platform) CrossSet() string`
  - `func (p Platform) GOARCH() string`
  - `func (p Platform) NixSystem() string`
  - `func (p Platform) Triple() string`
- `bob.Position`: Position is a file and line in a bob.go file, module-relative.
  - `func (p Position) String() string`
- `bob.Probe`: Probe decides when a service is ready.
  - `func (p Probe) Argv() []string`
  - `func (p Probe) Port() int`
- `bob.Profile`: Profile is the optimisation profile of a variant.
- `bob.Ref`: Ref is the typed result of Parse: a Label, a Pattern or a Configured value; String() returns the canonical form (SPEC §1.3, R-FW-015).
- `bob.RepoOpts`: RepoOpts are the repository-wide settings.
- `bob.RunOpts`: RunOpts declares a hermetic step.
- `bob.Sanitizer`: Sanitizer is a C/C++ (and cgo) sanitiser.
- `bob.Service`: Service is a loopback service a test starts before it runs (R-FW-025).
- `bob.ServiceOptions`: ServiceOptions configures ServiceFrom.
- `bob.ServiceSpec`: ServiceSpec describes one service.
- `bob.Shell`: Shell is the graph node of a DevShell.
- `bob.Sources`: Sources is a set of files from the repo, relative to the directory of the calling bob.go.
  - `func (s Sources) Union(o ...Sources) Sources`
- `bob.Step`: Step is the result of Run; it implements Tree.
  - `func (s *Step) File(p string) Tree`
- `bob.Test`: Test is a package whose build runs tests (R-FW-024).
- `bob.Tool`: Tool is a package that provides executables in bin/ for build steps and dev shells.
- `bob.Toolchain`: Toolchain describes the C/C++ compilers an ecosystem uses for one (host, target) pair (SPEC R-FW-033).
  - `func (t Toolchain) Cross() bool`
- `bob.Tree`: Tree is a package whose "out" output is a directory tree that other steps can read.
- `bob.Trigger`: Trigger says when a pipeline starts.
  - `func (t Trigger) String() string`
- `bob.Variant`: Variant is the configuration of one build request.
- `bob.VariantMod`: VariantMod changes a variant.

## nixpkgs

`import "github.com/megakuul/bob/pkg/nixpkgs"`

Functions:

- `func AllowInsecure(ids ...string) Option`
- `func AllowUnfree() Option`
- `func Cross() Option`
- `func Exe(p *bob.P, attr, mainProgram string, opts ...Option) *Program`
- `func Lib(p *bob.P, attr string, opts ...Option) *Package`
- `func Modules(names ...string) Option`
- `func Outputs(names ...string) Option`
- `func Pkg(p *bob.P, attr string, opts ...Option) *Package`
- `func Tool(p *bob.P, attr string, opts ...Option) *ToolPackage`

Types and methods:

- `nixpkgs.Option`: Option changes how a nixpkgs package is used.
- `nixpkgs.Package`: Package is a nixpkgs package of the target set (Lib, Pkg).
  - `func (pk *Package) CrossSet(p *bob.P) *Package`
  - `func (p *Package) LinkInfo() bob.LinkInfo`
  - `func (p *Package) Outputs() []bob.Output`
- `nixpkgs.Program`: Program is a nixpkgs package of the target set used as an Executable (Exe).
  - `func (x *Program) MainProgram() string`
- `nixpkgs.ToolPackage`: ToolPackage is a nixpkgs package of the host set used at build time (Tool).
  - `func (t *ToolPackage) Outputs() []bob.Output`

## golang

`import "github.com/megakuul/bob/pkg/golang"`

Functions:

- `func GenLabel(dir string) string`
- `func GeneratorRuntime(path, attr string, exact bool) Requirement`
- `func NewModule(p *bob.P, o ModuleOpts) *Module`
- `func Require(path, version string) Requirement`

Types and methods:

- `golang.Binary`: Binary is a Go program (label //<module dir>/<dir>:<name>).
  - `func (b *Binary) MainProgram() string`
- `golang.BinaryOpts`: BinaryOpts configures a main package.
- `golang.CGo`: CGo selects cgo for a binary's closure.
- `golang.CLib`: CLib is a Go main package built as a C library (-buildmode=c-archive or c-shared); it is a bob.Linkable (cc.Linkable) for C, C++ and Rust consumers.
  - `func (c *CLib) LinkInfo() bob.LinkInfo`
- `golang.CgoOpts`: CgoOpts gives a cgo package its typed C/C++/Rust dependencies (R-GO-001, R-RS-001).
- `golang.Generated`: Generated is a codegen recipe for a Go module (connect.Go, graphql.Gqlgen, openapi.Go): the module realises it as the package //<module dir>:gen-<dir without a leading "gen/", '/'→'-'> whose output is the Go package tree in Dir.
- `golang.Module`: Module is one Go module (label //<dir>:module).
  - `func (m *Module) Binary(dir string, o BinaryOpts) *Binary`
  - `func (m *Module) CArchive(dir string, o BinaryOpts) *CLib`
  - `func (m *Module) CShared(dir string, o BinaryOpts) *CLib`
  - `func (m *Module) Cgo(dir string, o CgoOpts) *Package`
  - `func (m *Module) Tests(pattern string, o TestOpts) *TestSet`
- `golang.ModuleAware`: ModuleAware is implemented by a Generated.Spec that needs its consuming module (the Go import path of the generated code): NewModule stores spec.WithModule(<module path>) as the codegen package's spec.
- `golang.ModuleOpts`: ModuleOpts describes one Go module; its go.mod/go.sum are generated from it (R-GO-003).
- `golang.Package`: Package is a Go package with a declaration of its own (a cgo package and its C dependencies).
- `golang.Requirement`: Requirement is one direct module requirement.
- `golang.TestOpts`: TestOpts configures a test set.
- `golang.TestSet`: TestSet is a set of test units, one per package.

## cc

`import "github.com/megakuul/bob/pkg/cc"`

Functions:

- `func External(p *bob.P, name string, o ExternalOpts) *ExternalLib`
- `func NewBinary(p *bob.P, name string, o BinaryOpts) *Binary`
- `func NewTest(p *bob.P, name string, o BinaryOpts) *Test`
- `func SharedLibrary(p *bob.P, name string, o LibraryOpts) *Library`
- `func StaticLibrary(p *bob.P, name string, o LibraryOpts) *Library`

Types and methods:

- `cc.Binary`: Binary is a C/C++ program (bin/<name>).
  - `func (b *Binary) MainProgram() string`
- `cc.BinaryOpts`: BinaryOpts configures NewBinary and NewTest.
- `cc.BuildSystem`: BuildSystem selects how an External is built inside its derivation.
- `cc.ExternalLib`: ExternalLib is a third-party library built by an External; it implements Linkable.
  - `func (e *ExternalLib) LinkInfo() bob.LinkInfo`
- `cc.ExternalOpts`: ExternalOpts describes a third-party C/C++ project with its own build system.
- `cc.LibSource`: LibSource is where the nixpkgs libraries (nixpkgs.Lib) of a cross-compiled package come from (SPEC R-FW-036).
- `cc.Library`: Library is a C/C++ library built by bob; it implements Linkable.
  - `func (l *Library) LinkInfo() bob.LinkInfo`
- `cc.LibraryOpts`: LibraryOpts configures StaticLibrary and SharedLibrary.
- `cc.Linkable`: Linkable is anything a C/C++/cgo/Rust/OCaml consumer can link: bob C/C++ libraries, Rust StaticLib/CDylib, cc.External, nixpkgs.Lib.
- `cc.Std`: Std is the language standard (-std=…).
- `cc.Test`: Test is a C/C++ test program and its run; building it runs the test.

## rust

`import "github.com/megakuul/bob/pkg/rust"`

Functions:

- `func Binary(p *bob.P, o BinOpts) *Bin`
- `func CDylib(p *bob.P, o LibOpts) *CLib`
- `func CfgTable(triple string) ([]string, error)`
- `func Crate(name, req string, opts ...DepOpt) Dep`
- `func Features(fs ...string) DepOpt`
- `func Library(p *bob.P, o LibOpts) *Lib`
- `func NoDefaultFeatures() DepOpt`
- `func ProcMacro(p *bob.P, o LibOpts) *Lib`
- `func Rename(codeName string) DepOpt`
- `func StaticLib(p *bob.P, o LibOpts) *CLib`
- `func Test(p *bob.P, o TestOpts) *TestSet`
- `func Uses(crate string, libs ...bob.Linkable) Option`

Types and methods:

- `rust.Bin`: Bin is a Rust program (label //<dir>:<Name>); its output holds bin/<Name>.
  - `func (b *Bin) MainProgram() string`
- `rust.BinOpts`: BinOpts configures Binary.
- `rust.CLib`: CLib is a Rust staticlib or cdylib (label //<dir>:<Name>): lib<name>.a or lib<name>.so plus the Headers as include/.
  - `func (*CLib) LinkInfo() bob.LinkInfo`
- `rust.Dep`: Dep is a dependency of a crate: a crates.io crate (Crate) or a local crate (*Lib, (*Lib).With).
- `rust.DepOpt`: DepOpt modifies a crates.io dependency.
- `rust.Edition`: Edition is a Rust edition.
- `rust.Lib`: Lib is a local Rust library or proc-macro crate (label //<dir>:<Name>), usable as Dep.
  - `func (l *Lib) With(features ...string) Dep`
- `rust.LibOpts`: LibOpts configures Library, ProcMacro, StaticLib and CDylib.
- `rust.Option`: Option is a graph-wide setting attached to any local crate's Options.
- `rust.TestOpts`: TestOpts selects what to test: the unit tests of Crate (--test) and every tests/*.rs integration test (one unit per file).
- `rust.TestSet`: TestSet is a set of Rust test units, or one unit when the crate has no integration tests.

## npm

`import "github.com/megakuul/bob/pkg/npm"`

Functions:

- `func Alias(as, name, req string) Dependency`
- `func AllowScripts(pkgs ...string) Option`
- `func CheckQuirks(table []Quirk) error`
- `func Dep(name, req string, opts ...DepOpt) Dependency`
- `func Dev() DepOpt`
- `func Extend(pkg string, deps ...Dependency) Option`
- `func Hoisted() Option`
- `func Library(p *bob.P, o LibOpts) *Lib`
- `func NewApp(p *bob.P, o AppOpts) *App`
- `func NewTest(p *bob.P, o TestOpts) *Test`
- `func Optional() DepOpt`
- `func Peer(name string) Dependency`
- `func Quirks() ([]Quirk, error)`
- `func Vite(p *bob.P, o ViteOpts) *Site`

Types and methods:

- `npm.App`: App is a Node/Bun program: bin/<Name> calling the runtime by store path.
  - `func (a *App) MainProgram() string`
- `npm.AppOpts`: AppOpts configures App.
- `npm.DepOpt`: DepOpt modifies a Dependency.
- `npm.Dependency`: Dependency is a registry package, an alias, a peer, or a local *Lib.
- `npm.DevServer`: DevServer configures `bob dev`.
- `npm.GenRuntime`: GenRuntime is an npm package generated code imports, at the version of the generator program Generator (a nixpkgs attribute) in bob's nixpkgs pin: bob lock adds it to the importer at exactly that version (@bufbuild/protobuf = protoc-gen-es).
- `npm.Generated`: Generated is a codegen recipe for an importer (connect.TS, graphql.TS, openapi.TS): the importer realises it as the package //<importer dir>:gen-<dir without a leading "gen/", '/'→'-'> whose output is the content of Dir.
- `npm.Lib`: Lib is a TypeScript library importer.
- `npm.LibOpts`: LibOpts configures Library.
- `npm.Option`: Option is a package-level fix declared by the user (not a quirk).
- `npm.Quirk`: Quirk is one curated entry; the table is Go data shipped with bob (D32).
- `npm.Runner`: Runner selects the test runner.
- `npm.Runtime`: Runtime selects the runtime of an App.
- `npm.Site`: Site is a Vite importer; its output is the static site directory.
- `npm.TSConfig`: TSConfig generates tsconfig.json (R-NPM-008).
- `npm.Test`: Test runs vitest or node --test over an importer.
- `npm.TestOpts`: TestOpts configures Test.
- `npm.ViteOpts`: ViteOpts configures Vite.

## ocaml

`import "github.com/megakuul/bob/pkg/ocaml"`

Functions:

- `func Conf(opamName string, libs ...cc.Linkable) Option`
- `func Executable(p *bob.P, o ExeOpts) *Exe`
- `func Library(p *bob.P, o LibOpts) *Lib`
- `func NewTest(p *bob.P, o ExeOpts) *Test`
- `func Opam(name, req string) *OpamPkg`

Types and methods:

- `ocaml.Dep`: Dep is an opam package, a findlib library of one, or a local *Lib.
- `ocaml.Exe`: Exe is a native OCaml executable (label //<dir>:<Name>); its output holds bin/<Name>.
  - `func (e *Exe) MainProgram() string`
- `ocaml.ExeOpts`: ExeOpts configures Executable and Test.
- `ocaml.Lib`: Lib is a local OCaml library (label //<dir>:<Name>), usable as Dep.
- `ocaml.LibOpts`: LibOpts configures Library.
- `ocaml.OpamPkg`: OpamPkg is an opam package resolved into bob.lock by bob lock.
  - `func (o *OpamPkg) Lib(findlib string) Dep`
- `ocaml.Option`: Option is a graph-wide setting attached to a local library or executable.
- `ocaml.Test`: Test is an OCaml test executable and its run; building it runs the test.

## proto

`import "github.com/megakuul/bob/pkg/proto"`

Functions:

- `func Breaking(p *bob.P, lib *Library, againstRev string) *Check`
- `func Lint(p *bob.P, lib *Library) *Check`
- `func NewLibrary(p *bob.P, o LibraryOpts) *Library`

Types and methods:

- `proto.Check`: Check is a buf lint or buf breaking check of a library: a test unit, run by bob test and in pipelines (bob build leaves it out).
- `proto.Library`: Library is one proto package (one directory, label //<dir>:proto).
  - `func (l *Library) Closure() []*Library`
  - `func (l *Library) Dir() string`
  - `func (l *Library) ImportRoot() string`
  - `func (l *Library) Imports() []*Library`
- `proto.LibraryOpts`: LibraryOpts configures a protobuf library: the *.proto files of the calling bob.go's directory.

## connect

`import "github.com/megakuul/bob/pkg/connect"`

Functions:

- `func Go(lib *proto.Library, o GoOpts) golang.Generated`
- `func TS(lib *proto.Library, o TSOpts) npm.Generated`

Types and methods:

- `connect.GoOpts`: GoOpts configures connect.Go.
- `connect.TSOpts`: TSOpts configures connect.TS.

## graphql

`import "github.com/megakuul/bob/pkg/graphql"`

Functions:

- `func Gqlgen(s *Schema, o GqlgenOpts) golang.Generated`
- `func NewSchema(p *bob.P, files bob.Sources) *Schema`
- `func TS(s *Schema, o TSOpts) npm.Generated`

Types and methods:

- `graphql.GqlgenOpts`: GqlgenOpts configures Gqlgen.
- `graphql.Schema`: Schema is a set of .graphqls files (label //<dir>:graphql).
- `graphql.TSOpts`: TSOpts configures TS.

## openapi

`import "github.com/megakuul/bob/pkg/openapi"`

Functions:

- `func Go(s *Spec, o GoOpts) golang.Generated`
- `func NewSpec(p *bob.P, file string) *Spec`
- `func TS(s *Spec, o TSOpts) npm.Generated`

Types and methods:

- `openapi.GoOpts`: GoOpts configures Go.
- `openapi.Server`: Server selects the server code oapi-codegen generates.
- `openapi.Spec`: Spec is one OpenAPI document (label //<dir>:openapi-<file name without extension>).
- `openapi.TSOpts`: TSOpts configures TS.

## services/postgres

`import "github.com/megakuul/bob/pkg/services/postgres"`

Functions:

- `func Service(p *bob.P, o Options) bob.Service`

Types and methods:

- `postgres.Options`: Options configures the PostgreSQL service.

## output/oci

`import "github.com/megakuul/bob/pkg/output/oci"`

Functions:

- `func NewImage(p *bob.P, name string, opts ImageOpts) *Image`

Types and methods:

- `oci.Image`: Image is an OCI image built from closures, without Docker.
- `oci.ImageOpts`: ImageOpts configures an image.

## output/tarball

`import "github.com/megakuul/bob/pkg/output/tarball"`

Functions:

- `func New(p *bob.P, name string, opts TarballOpts) *Tarball`

Types and methods:

- `tarball.Kind`: Kind selects what a tarball contains.
- `tarball.Tarball`: Tarball is a `<name>-<platform>.tar.gz` output (R-OUT-004).
- `tarball.TarballOpts`: TarballOpts configures a tarball.

## output/appimage

`import "github.com/megakuul/bob/pkg/output/appimage"`

Functions:

- `func NewBundle(p *bob.P, name string, opts BundleOpts) *Bundle`

Types and methods:

- `appimage.Bundle`: Bundle is a self-contained AppImage for one platform.
- `appimage.BundleOpts`: BundleOpts configures a bundle.
- `appimage.Desktop`: Desktop describes the .desktop entry.
- `appimage.Mode`: Mode restricts the launcher.

## release

`import "github.com/megakuul/bob/pkg/release"`

Functions:

- `func New(p *bob.P, name string, opts ReleaseOpts) *Release`

Types and methods:

- `release.Release`: Release is a versioned set of artifacts.
- `release.ReleaseOpts`: ReleaseOpts configures a release.

## Next steps

- [bob.go reference](../04-guides/01-bob-go-reference.md): how to use these packages.
- [Go quickstart](../03-quickstart/01-go.md): a complete project.
