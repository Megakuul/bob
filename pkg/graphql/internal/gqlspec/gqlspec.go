// Package gqlspec holds the IR payloads ("spec") of the GraphQL node kinds. pkg/graphql writes
// them during evaluation, pkg/graphql/gen reads them when planning. Dependency references are
// indices into the node's deps.
package gqlspec

// Node kinds of the GraphQL generators (SPEC §03.6.1).
const (
	KindSchema    = "graphql.schema"    // a set of .graphqls files (the node's sources)
	KindDocuments = "graphql.documents" // operation documents of a graphql.ts node (its sources)
	KindGqlgen    = "graphql.gqlgen"    // gqlgen output realised into a Go module
	KindTS        = "graphql.ts"        // graphql-codegen output realised into an npm importer
)

// The Go runtime of the gqlgen output (§03.6.4): gqlgen at exactly the generator's version
// (bob lock reads it from the nixpkgs pin, golang.GeneratorRuntime) and at least the gqlparser
// gqlgen 0.17.90's go.mod requires (the generated code imports gqlparser directly).
const (
	GqlgenModule     = "github.com/99designs/gqlgen"
	GqlparserModule  = "github.com/vektah/gqlparser/v2"
	GqlparserVersion = "v2.5.33"
)

// Schema is the spec of a graphql.schema node.
type Schema struct{}

// Gqlgen is the spec of a graphql.gqlgen node. Deps: the schema, gqlgen, go.
type Gqlgen struct {
	Schema   int               `json:"schema"`
	Tool     int               `json:"tool"`
	Go       int               `json:"go"`
	Models   map[string]string `json:"models"`   // GraphQL type → Go type
	Autobind []string          `json:"autobind"` // Go packages
}

// TS is the spec of a graphql.ts node. Deps: the schema, the documents node when there are
// documents (Documents = its index, -1 = none), nodejs, bash (the tool lock's installer).
type TS struct {
	Schema    int               `json:"schema"`
	Documents int               `json:"documents"`
	Node      int               `json:"node"`
	Bash      int               `json:"bash"`
	Scalars   map[string]string `json:"scalars"` // GraphQL scalar → TS type
}
