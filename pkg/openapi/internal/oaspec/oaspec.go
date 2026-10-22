// Package oaspec holds the IR payloads ("spec") of the OpenAPI node kinds. pkg/openapi writes
// them during evaluation, pkg/openapi/gen reads them when planning. Dependency references are
// indices into the node's deps.
package oaspec

// Node kinds of the OpenAPI generators (SPEC §03.6.1).
const (
	KindSpec = "openapi.spec" // one OpenAPI document (its file is the node's source)
	KindGo   = "openapi.go"   // oapi-codegen output realised into a Go module
	KindTS   = "openapi.ts"   // openapi-typescript output realised into an npm importer
)

// RuntimeModule and RuntimeVersion are the Go runtime the oapi-codegen output imports
// (§03.6.4). oapi-codegen 2.5.1 (the nixpkgs pin) generates code for runtime v1.1.x (S6).
const (
	RuntimeModule  = "github.com/oapi-codegen/runtime"
	RuntimeVersion = "v1.1.2"
)

// Spec is the spec of an openapi.spec node.
type Spec struct {
	File string `json:"file"` // repository-relative path of the document
}

// Go is the spec of an openapi.go node. Deps: the spec node, oapi-codegen.
type Go struct {
	Spec    int    `json:"spec"`
	Tool    int    `json:"tool"`
	Package string `json:"package"`
	Server  string `json:"server"` // "" (none) or "std-http"
	Models  bool   `json:"models"`
	Client  bool   `json:"client"`
}

// Servers.
const ServerStdHTTP = "std-http"

// TS is the spec of an openapi.ts node. Deps: the spec node, nodejs, bash (the tool lock's
// installer).
type TS struct {
	Spec int `json:"spec"`
	Node int `json:"node"`
	Bash int `json:"bash"`
}
