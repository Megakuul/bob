// Package protospec holds the IR payloads of the protobuf kinds (written by pkg/proto, read by
// pkg/proto/gen and pkg/connect/gen through their own copies of these JSON shapes).
package protospec

// Node kinds; "proto" is their ecosystem.
const (
	KindLibrary = "proto.library" // one proto package: the .proto files of one directory
	KindCheck   = "proto.check"   // buf lint / buf breaking of a library, a test unit
)

// Library is the spec of a proto.library node. Deps: the imported libraries, in order; the
// node's sources are its .proto files.
type Library struct {
	Root string `json:"root"` // import root, repo-relative ("" = repository root)
	Dir  string `json:"dir"`  // the library's directory, repo-relative
}

// Check modes.
const (
	Lint     = "lint"
	Breaking = "breaking"
)

// Check is the spec of a proto.check node. Deps: the library (index 0), then buf (index 1).
type Check struct {
	Mode    string `json:"mode"`
	Against string `json:"against,omitempty"` // Breaking: git revision of this repository
}
