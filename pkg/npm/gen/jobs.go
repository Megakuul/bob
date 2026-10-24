package gen

// The JSON of the npm job kinds (pkg/npm/bop). Generators write the jobs themselves and do
// not import the builder layer (R-ARCH-003); plan_test.go decodes every planned job strictly
// into bop's types.

type link struct {
	Alias  string `json:"alias"`
	Target string `json:"target"`
}

type bin struct {
	Name   string `json:"name"`
	Script string `json:"script"`
}

type script struct {
	Node string            `json:"node"`
	Bash string            `json:"bash"`
	Path []string          `json:"path"`
	Bins []bin             `json:"bins"`
	Env  map[string]string `json:"env"`
}

type member struct {
	Dir     string  `json:"dir"`
	Name    string  `json:"name"`
	Tarball string  `json:"tarball"`
	Links   []link  `json:"links"`
	Script  *script `json:"script,omitempty"`
}

type unpackArgs struct {
	Members []member `json:"members"`
}

type hoistEntry struct {
	Path    string  `json:"path"`
	Tarball string  `json:"tarball,omitempty"`
	Link    string  `json:"link,omitempty"`
	Script  *script `json:"script,omitempty"`
}

type nodeModulesArgs struct {
	Out     string       `json:"out"`
	Links   []link       `json:"links"`
	Hoisted []hoistEntry `json:"hoisted"`
	Bins    []bin        `json:"bins"`
	Node    string       `json:"node"`
	Bash    string       `json:"bash"`
}

type step struct {
	Op      string            `json:"op"`
	Path    string            `json:"path"`
	From    string            `json:"from,omitempty"`
	Target  string            `json:"target,omitempty"`
	Content string            `json:"content,omitempty"`
	Mode    uint32            `json:"mode,omitempty"`
	Argv    []string          `json:"argv,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Dirs    []string          `json:"dirs,omitempty"`
}

type buildArgs struct {
	Steps []step `json:"steps"`
}

// testRunArgs is bspec.TestRunArgs (the core test-run kind).
type testRunArgs struct {
	Argv     []string          `json:"argv"`
	Path     []string          `json:"path,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Copy     []copyArgs        `json:"copy,omitempty"`
	Format   string            `json:"format,omitempty"`
	Services []serviceJob      `json:"services,omitempty"`
	TimeoutS int64             `json:"timeoutSeconds,omitempty"`
}

type copyArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type serviceJob struct {
	Name      string            `json:"name"`
	Argv      []string          `json:"argv"`
	Path      []string          `json:"path,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Ready     probeJob          `json:"ready"`
	TimeoutMs int64             `json:"timeoutMs"`
}

type probeJob struct {
	Port int      `json:"port,omitempty"`
	Argv []string `json:"argv,omitempty"`
}
