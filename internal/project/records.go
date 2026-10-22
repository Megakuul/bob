package project

import (
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Record is proj/<projectID> (SPEC §05.4.2 ProjectRecord), the truth about a project.
type Record struct {
	ID   string `pk:"proj" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`

	DisplayName string `cbor:"dn,omitempty"`
	Description string `cbor:"ds,omitempty"`
	// The linked repository (R-PIPE-002); Forge == "" means none (remote builds only).
	Forge         string    `cbor:"fg,omitempty"`
	RepoName      string    `cbor:"rn,omitempty"`
	RepoID        int64     `cbor:"ri,omitempty"`
	DefaultBranch string    `cbor:"db,omitempty"`
	WebURL        string    `cbor:"wu,omitempty"`
	HookErr       string    `cbor:"he,omitempty"` // why bob's webhook could not be ensured
	Created       time.Time `cbor:"cr,omitzero"`
	// Slot is the project's position in the projects index plus one (0 = none yet).
	Slot uint64 `cbor:"ix,omitempty"`
}

// Repo is the linked repository as JobSpec.Repo and the build controller name it:
// "<forgeID>/<repoID>", or "" without a link.
func (r *Record) Repo() string {
	if r.Forge == "" || r.RepoID == 0 {
		return ""
	}
	return r.Forge + "/" + itoa(r.RepoID)
}

// IndexCounter is pidxm/all: the number of slots allocated in the projects index.
type IndexCounter struct {
	Name  string `pk:"pidxm" cbor:"-"` // "all"
	ETag  string `etag:"true" cbor:"-"`
	Count uint64 `cbor:"n,omitempty"`
}

// Entry is one slot of the projects index pidx/all/p/<num10> (P = 200). It carries what the
// projects page shows, so a list costs no record reads (§05.5.2: 1 + pages). A zero entry is
// an unused (deleted) slot; a Pending entry is hidden until its record is written or removed
// (intent first, R-SRV-101), and index repair settles pending entries a crash left behind.
type Entry struct {
	ID            string    `cbor:"id,omitempty"`
	Pending       bool      `cbor:"pd,omitempty"`
	At            time.Time `cbor:"at,omitzero"` // when the entry was last written (repair age)
	DisplayName   string    `cbor:"dn,omitempty"`
	Description   string    `cbor:"ds,omitempty"`
	Forge         string    `cbor:"fg,omitempty"`
	RepoName      string    `cbor:"rn,omitempty"`
	RepoID        int64     `cbor:"ri,omitempty"`
	DefaultBranch string    `cbor:"db,omitempty"`
	WebURL        string    `cbor:"wu,omitempty"`
	Health        int32     `cbor:"h,omitempty"` // projectv1.LinkHealth
	Created       time.Time `cbor:"cr,omitzero"`
}

// indexPage is P of the projects index.
const indexPage = 200

// Index is the projects index pidx/all/p/<num10>.
func Index(control *store.Bucket) *store.PagedIndex[Entry] {
	return store.NewPagedIndex[Entry](control, "pidx", "all", indexPage)
}

// PipelinesRecord is ppl/<projectID>: the pipelines of the newest accepted evaluation of the
// project's default branch (ListPipelines), and the last build of each pipeline. The build
// controller's hooks write it; ListPipelines reads it with one conditional GET.
type PipelinesRecord struct {
	Project string `pk:"ppl" cbor:"-"`
	ETag    string `etag:"true" cbor:"-"`

	SHA     string        `cbor:"sha,omitempty"` // evaluated commit (or upload sha256)
	Build   string        `cbor:"b,omitempty"`   // the build whose evaluation this is
	N       uint64        `cbor:"n,omitempty"`   // its number
	Default bool          `cbor:"df,omitempty"`  // evaluated on the default branch
	At      time.Time     `cbor:"at,omitzero"`
	Defs    []PipelineDef `cbor:"d,omitempty"`
	// Last is the newest build that ran each pipeline, by pipeline name (≤ maxPipelines).
	Last map[string]LastRun `cbor:"l,omitempty"`
}

// PipelineDef is one pipeline as the evaluation reported it.
type PipelineDef struct {
	Name        string   `cbor:"n,omitempty"`
	Triggers    []string `cbor:"t,omitempty"`
	Concurrency string   `cbor:"c,omitempty"` // "" | queue | supersede | parallel
	Releases    bool     `cbor:"r,omitempty"`
}

// LastRun is the newest build of a pipeline.
type LastRun struct {
	Build string `cbor:"b,omitempty"`
	N     uint64 `cbor:"n,omitempty"`
	State string `cbor:"st,omitempty"` // BuildRecord state of the pipeline run
}

// maxPipelines bounds PipelinesRecord (hostile evaluation output is bounded upstream too).
const maxPipelines = 200
