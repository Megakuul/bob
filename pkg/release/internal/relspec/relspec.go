// Package relspec holds the IR payload of the release kind (written by pkg/release, read by
// pkg/release/gen).
package relspec

// KindRelease is the node kind of release.New; "release" is its ecosystem.
const KindRelease = "release.release"

// Release is the spec of a release.release node. Dependency references are indices into the
// node's deps; the node's source group, when present, is the Markdown notes file.
type Release struct {
	Name      string     `json:"name"`
	Public    bool       `json:"public"`
	Platforms []Platform `json:"platforms"` // closures per platform, sorted by platform
	Images    []int      `json:"images"`
	AppImages []int      `json:"appImages"`
	Tarballs  []int      `json:"tarballs"`
}

// Platform lists the closures of one platform.
type Platform struct {
	Platform string `json:"platform"`
	Closures []int  `json:"closures"`
}
