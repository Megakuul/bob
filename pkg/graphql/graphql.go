package graphql

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/graphql/internal/gqlspec"
	"github.com/megakuul/bob/pkg/npm"
)

// Kinds of the GraphQL generators. Generated code is the same for every platform.
func init() {
	for _, k := range []string{gqlspec.KindSchema, gqlspec.KindDocuments, gqlspec.KindGqlgen, gqlspec.KindTS} {
		ext.Register(ext.Kind{Name: k})
	}
}

// Schema is a set of .graphqls files (label //<dir>:graphql).
type Schema struct {
	bob.Node
	p *bob.P
}

// NewSchema declares the GraphQL schema made of files.
//
//	var API = bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })
func NewSchema(p *bob.P, files bob.Sources) *Schema {
	return &Schema{p: p, Node: ext.New(p, gqlspec.KindSchema, "graphql", nil, []bob.Sources{files}, gqlspec.Schema{})}
}

// GqlgenOpts configures Gqlgen.
type GqlgenOpts struct {
	Dir      string            // default "gen/graph" (+ "gen/graph/model")
	Models   map[string]string // GraphQL type → Go type ("github.com/google/uuid.UUID")
	Autobind []string          // Go packages
}

var (
	gqlName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`) })
	goType  = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*\.[A-Za-z_][A-Za-z0-9_]*$`)
	})
	goPkg = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*$`) })
)

// Gqlgen generates the executable schema and models with gqlgen (from the nixpkgs pin); never
// resolver stubs. Adds github.com/99designs/gqlgen (exact generator version) and its gqlparser.
//
//	golang.ModuleOpts{Generated: []golang.Generated{graphql.Gqlgen(schema.API(p), graphql.GqlgenOpts{})}}
func Gqlgen(s *Schema, o GqlgenOpts) golang.Generated {
	if s == nil || s.p == nil {
		ext.Errorf("graphql.Gqlgen: the schema is nil")
		return golang.Generated{}
	}
	for _, k := range slices.Sorted(maps.Keys(o.Models)) {
		if !gqlName().MatchString(k) || !goType().MatchString(o.Models[k]) {
			ext.Errorf("graphql.GqlgenOpts.Models: %q → %q must map a GraphQL type to a Go type (\"example.com/m/pkg.Type\")", k, o.Models[k])
		}
	}
	for _, a := range o.Autobind {
		if !goPkg().MatchString(a) {
			ext.Errorf("graphql.GqlgenOpts.Autobind: %q is not a Go package path", a)
		}
	}
	models := map[string]string{}
	maps.Copy(models, o.Models)
	auto := slices.Clone(o.Autobind)
	if auto == nil {
		auto = []string{}
	}
	slices.Sort(auto)
	dir := o.Dir
	if dir == "" {
		dir = "gen/graph"
	}
	return golang.Generated{Kind: gqlspec.KindGqlgen, Dir: dir, At: ext.Here(),
		Deps: []bob.Package{s}, Tools: []string{"gqlgen", "go"},
		Spec: gqlspec.Gqlgen{Schema: 0, Tool: 1, Go: 2, Models: models, Autobind: slices.Compact(auto)},
		Require: []golang.Requirement{
			golang.GeneratorRuntime(gqlspec.GqlgenModule, "gqlgen", true),
			golang.Require(gqlspec.GqlparserModule, gqlspec.GqlparserVersion),
		}}
}

// validScalars checks TSOpts.Scalars.
func validScalars(m map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !gqlName().MatchString(k) || strings.TrimSpace(m[k]) == "" || strings.ContainsAny(m[k], "\n\r") {
			out = append(out, fmt.Sprintf("graphql.TSOpts.Scalars: %q → %q must map a GraphQL scalar to a TypeScript type", k, m[k]))
		}
	}
	return out
}

// TSOpts configures TS.
type TSOpts struct {
	Dir       string            // default "gen/graphql"
	Documents bob.Sources       // operation documents
	Scalars   map[string]string // GraphQL scalar → TS type
}

// TS runs graphql-codegen (typescript + typescript-operations) through bob's driver and tool
// lock (§03.6.5); the importer realises it as gen/graphql/graphql.ts.
//
//	npm.ViteOpts{Generated: []npm.Generated{graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")})}}
func TS(s *Schema, o TSOpts) npm.Generated {
	if s == nil || s.p == nil {
		ext.Errorf("graphql.TS: the schema is nil")
		return npm.Generated{}
	}
	for _, pr := range validScalars(o.Scalars) {
		ext.Errorf("%s", pr)
	}
	scalars := map[string]string{}
	maps.Copy(scalars, o.Scalars)
	deps := []bob.Package{s}
	spec := gqlspec.TS{Schema: 0, Documents: -1, Scalars: scalars}
	if !reflect.ValueOf(o.Documents).IsZero() {
		spec.Documents = len(deps)
		deps = append(deps, &documents{Node: ext.New(s.p, gqlspec.KindDocuments, "graphql-documents", nil, []bob.Sources{o.Documents}, struct{}{})})
	}
	spec.Node, spec.Bash = len(deps), len(deps)+1 // the Tools follow the Deps
	dir := o.Dir
	if dir == "" {
		dir = "gen/graphql"
	}
	return npm.Generated{Kind: gqlspec.KindTS, Dir: dir, Deps: deps, Tools: []string{"nodejs", "bash"}, Spec: spec, At: ext.Here()}
}

// documents are the operation documents of a TS recipe.
type documents struct{ bob.Node }
