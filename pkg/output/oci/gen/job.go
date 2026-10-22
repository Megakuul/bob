package gen

// The job arguments of the oci-layer and oci-image kinds, as pkg/output/oci/bop reads them
// (generators cannot import the builder layer; plan_test decodes every job strictly into bop's
// types).

// layerFormat is the layer format version (bop.LayerFormat); part of every layer derivation.
const layerFormat = "bob-oci-layer-v1"

type layerArgs struct {
	Format string   `json:"format"`
	Paths  []string `json:"paths"`
	Skip   []string `json:"skip"`
}

type imageArgs struct {
	Manifests []manifest `json:"manifests"`
	Tag       string     `json:"tag"`
	Error     string     `json:"error,omitempty"`
}

type manifest struct {
	Architecture string   `json:"architecture"`
	Layers       []string `json:"layers"`
	Custom       custom   `json:"custom"`
	Config       config   `json:"config"`
}

type custom struct {
	CACert   string `json:"caCert"`
	ZoneInfo string `json:"zoneInfo"`
	Licences string `json:"licences"`
}

type config struct {
	User         string            `json:"user"`
	WorkingDir   string            `json:"workingDir"`
	Env          []string          `json:"env"`
	Entrypoint   []string          `json:"entrypoint"`
	Cmd          []string          `json:"cmd"`
	ExposedPorts []string          `json:"exposedPorts"`
	Labels       map[string]string `json:"labels"`
}
