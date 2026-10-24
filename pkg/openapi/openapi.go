package openapi

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
	"github.com/megakuul/bob/pkg/npm"
	"github.com/megakuul/bob/pkg/openapi/internal/oaspec"
)

// Kinds of the OpenAPI generators. Generated code is the same for every platform.
func init() {
	ext.Register(ext.Kind{Name: oaspec.KindSpec, Unit: "openapi"}) // one spec per package (R-FW-071)
	for _, k := range []string{oaspec.KindGo, oaspec.KindTS} {
		ext.Register(ext.Kind{Name: k})
	}
}

// Spec is one OpenAPI document (label //<dir>:openapi-<file name without extension>).
type Spec struct {
	bob.Node
	p *bob.P
}

var (
	labelName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`) })
	goIdent   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z_][a-z0-9_]*$`) })
)

// NewSpec declares the OpenAPI document file (relative to the calling bob.go, YAML or JSON).
//
//	var Items = bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })
func NewSpec(p *bob.P, file string) *Spec {
	dir, _ := ext.CallerDir()
	clean := path.Clean(file)
	var problem string
	// A file outside the package is reported by the region rule of its sources (R-FW-070,
	// BOB-E-1028, the package named).
	if file == "" || clean == "." {
		problem = fmt.Sprintf("openapi.NewSpec(%q): name the spec file of this package", file)
		clean = "openapi.yaml"
	}
	base := strings.ToLower(strings.TrimSuffix(path.Base(clean), path.Ext(clean)))
	name := "openapi-" + base
	if !labelName().MatchString(name) {
		name = "openapi"
	}
	s := &Spec{p: p}
	s.Node = ext.New(p, oaspec.KindSpec, name, nil, []bob.Sources{bob.Files(clean)},
		oaspec.Spec{File: path.Join(dir, clean)})
	if problem != "" {
		ext.Errorf("%s: %s", s.DefinedAt(), problem)
	}
	return s
}

// Server selects the server code oapi-codegen generates.
type Server int

// Servers.
const (
	NoServer Server = iota
	StdHTTP         // net/http ServeMux (Go 1.22 patterns)
)

// GoOpts configures Go.
type GoOpts struct {
	Dir     string // default "gen/<Package>"
	Package string // required
	Server  Server
	Models  *bool // default true
	Client  bool
}

// Go runs oapi-codegen (from the nixpkgs pin) for s; the module realises it as the package in
// Dir. Adds github.com/oapi-codegen/runtime.
//
//	golang.ModuleOpts{Generated: []golang.Generated{openapi.Go(schema.Items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP})}}
func Go(s *Spec, o GoOpts) golang.Generated {
	if s == nil || s.p == nil {
		ext.Errorf("openapi.Go: the spec is nil")
		return golang.Generated{}
	}
	var problems []string
	if !goIdent().MatchString(o.Package) {
		problems = append(problems, fmt.Sprintf("openapi.GoOpts.Package %q must be a Go package name (lower case letters, digits, '_')", o.Package))
	}
	spec := oaspec.Go{Spec: 0, Tool: 1, Package: o.Package, Models: o.Models == nil || *o.Models, Client: o.Client}
	switch o.Server {
	case NoServer:
	case StdHTTP:
		spec.Server = oaspec.ServerStdHTTP
	default:
		problems = append(problems, fmt.Sprintf("openapi.GoOpts.Server %d is not a server kind", o.Server))
	}
	if !spec.Models && spec.Server == "" && !spec.Client {
		problems = append(problems, "openapi.GoOpts: nothing to generate (Models is false, no Server, no Client)")
	}
	for _, pr := range problems {
		ext.Errorf("%s", pr)
	}
	dir := o.Dir
	if dir == "" {
		dir = "gen/" + o.Package
	}
	spec.Dir = dir
	return golang.Generated{Kind: oaspec.KindGo, Dir: dir, Spec: spec, At: ext.Here(),
		Deps: []bob.Package{s}, Tools: []nixext.Attr{nixpkgs.OapiCodegen},
		Require: []golang.Requirement{golang.Require(oaspec.RuntimeModule, oaspec.RuntimeVersion)}}
}

// TSOpts configures TS.
type TSOpts struct{ Dir string } // default "gen/openapi"

// TS generates TypeScript types with openapi-typescript (bob's tool lock, §03.6.5); the
// importer realises them as gen/openapi/index.d.ts.
//
//	npm.ViteOpts{Generated: []npm.Generated{openapi.TS(schema.Items(p), openapi.TSOpts{})}}
func TS(s *Spec, o TSOpts) npm.Generated {
	if s == nil || s.p == nil {
		ext.Errorf("openapi.TS: the spec is nil")
		return npm.Generated{}
	}
	dir := o.Dir
	if dir == "" {
		dir = "gen/openapi"
	}
	return npm.Generated{Kind: oaspec.KindTS, Dir: dir, Deps: []bob.Package{s}, Tools: []nixext.Attr{nixpkgs.Nodejs, nixpkgs.Bash},
		Spec: oaspec.TS{Dir: dir, Spec: 0, Node: 1, Bash: 2}, At: ext.Here()}
}
