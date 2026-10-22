package bop

// LayerFormat is the layer format version; it is part of every layer derivation (R-OUT-011).
const LayerFormat = "bob-oci-layer-v1"

// LayerArgs is the Args schema of kind "oci-layer": one layer holding Paths (store paths) without
// the entries in Skip (pruned files or directories, absolute store paths, R-OUT-007).
type LayerArgs struct {
	Format string   `json:"format"`
	Paths  []string `json:"paths"`
	Skip   []string `json:"skip"`
}

// LayerInfo is $out/layer.json of a layer derivation.
type LayerInfo struct {
	DiffID string   `json:"diffID"`
	Digest string   `json:"digest"`
	Size   int64    `json:"size"`
	Paths  []string `json:"paths"`
}

// ImageArgs is the Args schema of kind "oci-image": an OCI image layout with one image index
// over Manifests (in this order). Tag, when set, names the index in index.json
// (org.opencontainers.image.ref.name).
type ImageArgs struct {
	Manifests []Manifest `json:"manifests"`
	Tag       string     `json:"tag"`
	// Error, when set, fails the job with it: the derivation bob plans when the image's programs
	// could not be built while planning (it only runs if those builds succeed later).
	Error string `json:"error,omitempty"`
}

// Manifest is one platform's image: the layer outputs in order, then the customisation layer
// built from Custom, and the image configuration.
type Manifest struct {
	Architecture string   `json:"architecture"` // amd64 | arm64
	Layers       []string `json:"layers"`       // layer derivation outputs
	Custom       Custom   `json:"custom"`
	Config       Config   `json:"config"`
}

// Custom is the content of the customisation layer (R-OUT-012).
type Custom struct {
	CACert   string `json:"caCert"`   // target of the /etc/ssl/certs symlinks; "" = none
	ZoneInfo string `json:"zoneInfo"` // target of /usr/share/zoneinfo; "" = none
	Licences string `json:"licences"` // content of /.bob/licences.json
}

// Config is the "config" object of the OCI image configuration.
type Config struct {
	User         string            `json:"user"`
	WorkingDir   string            `json:"workingDir"`
	Env          []string          `json:"env"`
	Entrypoint   []string          `json:"entrypoint"`
	Cmd          []string          `json:"cmd"`
	ExposedPorts []string          `json:"exposedPorts"`
	Labels       map[string]string `json:"labels"`
}
