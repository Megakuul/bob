// Package tarspec holds the IR payload of the tarball kind (written by pkg/output/tarball,
// read by pkg/output/tarball/gen).
package tarspec

// KindTarball is the node kind of tarball.New; "tarball" is its ecosystem.
const KindTarball = "tarball.tarball"

// Kinds of tarball.
const (
	Closure = "closure"
	Static  = "static"
)

// Tarball is the spec of a tarball.tarball node; its only dependency (index 0) is the program
// built for Platform.
type Tarball struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
}
