// Package ocispec holds the IR payload of the OCI image kind. pkg/output/oci writes it during
// evaluation, pkg/output/oci/gen reads it when planning. Dependency references are indices into
// the node's deps.
package ocispec

// KindImage is the node kind of oci.NewImage; "oci" is its ecosystem.
const KindImage = "oci.image"

// Image is the spec of an oci.image node.
type Image struct {
	Name       string            `json:"name"`
	Platforms  []Platform        `json:"platforms"` // sorted by platform (amd64 first)
	Entrypoint []string          `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
	Env        map[string]string `json:"env"`
	User       string            `json:"user"`    // "" = 65534:65534
	WorkDir    string            `json:"workDir"` // "" = /
	Ports      []int             `json:"ports"`
	Labels     map[string]string `json:"labels"`
	MaxLayers  int               `json:"maxLayers"` // 0 = 100
	Prune      []string          `json:"prune"`
	CACert     int               `json:"caCert"` // dep index of nixpkgs cacert, -1 = NoCACerts
	TZData     int               `json:"tzData"` // dep index of nixpkgs tzdata, -1 = NoTZData
}

// Platform is the program and extra roots of one platform of the image.
type Platform struct {
	Platform string `json:"platform"`
	Program  int    `json:"program"`
	Extra    []int  `json:"extra"`
}
