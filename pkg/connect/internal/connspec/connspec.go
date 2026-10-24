// Package connspec holds the IR payload of the connect code generators (written by
// pkg/connect, read by pkg/connect/gen).
package connspec

// Node kinds; "connect" is their ecosystem.
const (
	KindGo = "connect.go" // protoc-gen-go + protoc-gen-connect-go into a Go module
	KindTS = "connect.ts" // protoc-gen-es into an npm importer
)

// Gen is the spec of a connect.go or connect.ts node. Deps: the protobuf library (index 0),
// then buf and the protoc plugins (the consumer appends the recipe's Tools).
type Gen struct {
	Dir    string `json:"dir"`    // owned directory relative to the consumer
	Simple bool   `json:"simple"` // connect-go simple API
	// Go only: the consuming module's path (golang.ModuleAware) and the directory, relative to
	// the module, of the Go package of each library of the closure (key: library directory,
	// repo-relative): protoc-gen-go's M options.
	Module  string            `json:"module,omitempty"`
	Imports map[string]string `json:"imports,omitempty"`
}

// WithModule implements golang.ModuleAware.
func (g Gen) WithModule(path string) any {
	g.Module = path
	return g
}
