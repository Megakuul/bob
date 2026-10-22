package project

import (
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Record is proj/<projectID> (SPEC §05.4.2 ProjectRecord), the truth about a project.
type Record struct {
	ID   store.KeyField  `pk:"proj" cbor:"-"`
	ETag store.ETagField `etag:"true" cbor:"-"`

	DisplayName store.DataField[string] `cbor:"dn,omitempty"`
	Description store.DataField[string] `cbor:"ds,omitempty"`
	// The linked repository (R-PIPE-002); Forge == "" means none (remote builds only).
	Forge         store.DataField[string]    `cbor:"fg,omitempty"`
	RepoName      store.DataField[string]    `cbor:"rn,omitempty"`
	RepoID        store.DataField[int64]     `cbor:"ri,omitempty"`
	DefaultBranch store.DataField[string]    `cbor:"db,omitempty"`
	WebURL        store.DataField[string]    `cbor:"wu,omitempty"`
	HookErr       store.DataField[string]    `cbor:"he,omitempty"` // why bob's webhook could not be ensured
	Created       store.DataField[time.Time] `cbor:"cr,omitempty"`
	// Slot is the project's position in the projects index plus one (0 = none yet).
	Slot store.DataField[uint64] `cbor:"ix,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *Record) Draft() *Record {
	return &Record{
		ID:            r.ID,
		ETag:          r.ETag,
		DisplayName:   store.Carry(r.DisplayName),
		Description:   store.Carry(r.Description),
		Forge:         store.Carry(r.Forge),
		RepoName:      store.Carry(r.RepoName),
		RepoID:        store.Carry(r.RepoID),
		DefaultBranch: store.Carry(r.DefaultBranch),
		WebURL:        store.Carry(r.WebURL),
		HookErr:       store.Carry(r.HookErr),
		Created:       store.Carry(r.Created),
		Slot:          store.Carry(r.Slot),
	}
}

// Repo is the linked repository as JobSpec.Repo and the build controller name it:
// "<forgeID>/<repoID>", or "" without a link.
func (r *Record) Repo() string {
	if r.Forge.Value() == "" || r.RepoID.Value() == 0 {
		return ""
	}
	return r.Forge.Value() + "/" + itoa(r.RepoID.Value())
}

// IndexCounter is pidxm/all: the number of slots allocated in the projects index.
type IndexCounter struct {
	Name  store.KeyField          `pk:"pidxm" cbor:"-"` // "all"
	ETag  store.ETagField         `etag:"true" cbor:"-"`
	Count store.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *IndexCounter) Draft() *IndexCounter {
	return &IndexCounter{
		Name:  r.Name,
		ETag:  r.ETag,
		Count: store.Carry(r.Count),
	}
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
	return store.NewPagedIndex(control, indexPage, func(k uint64, e []Entry) *indexPageRecord {
		n, _ := store.Num10(k)
		p := &indexPageRecord{Name: store.ID("all"), Page: store.ID(n)}
		if len(e) > 0 {
			p.Entries = store.Set(e)
		}
		return p
	}, func(p *indexPageRecord) []Entry { return p.Entries.Value() })
}

// indexPageRecord is one page pidx/all/p/<num10> of the projects index.
type indexPageRecord struct {
	Name    store.KeyField           `pk:"pidx" cbor:"-"` // "all"
	Page    store.KeyField           `sk:"p" cbor:"-"`
	ETag    store.ETagField          `etag:"true" cbor:"-"`
	Entries store.DataField[[]Entry] `cbor:"e,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *indexPageRecord) Draft() *indexPageRecord {
	return &indexPageRecord{Name: r.Name, Page: r.Page, ETag: r.ETag, Entries: store.Carry(r.Entries)}
}

// PipelinesRecord is ppl/<projectID>: the pipelines of the newest accepted evaluation of the
// project's default branch (ListPipelines), and the last build of each pipeline. The build
// controller's hooks write it; ListPipelines reads it with one conditional GET.
type PipelinesRecord struct {
	Project store.KeyField  `pk:"ppl" cbor:"-"`
	ETag    store.ETagField `etag:"true" cbor:"-"`

	SHA     store.DataField[string]        `cbor:"sha,omitempty"` // evaluated commit (or upload sha256)
	Build   store.DataField[string]        `cbor:"b,omitempty"`   // the build whose evaluation this is
	N       store.DataField[uint64]        `cbor:"n,omitempty"`   // its number
	Default store.DataField[bool]          `cbor:"df,omitempty"`  // evaluated on the default branch
	At      store.DataField[time.Time]     `cbor:"at,omitempty"`
	Defs    store.DataField[[]PipelineDef] `cbor:"d,omitempty"`
	// Last is the newest build that ran each pipeline, by pipeline name (≤ maxPipelines).
	Last store.DataField[map[string]LastRun] `cbor:"l,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *PipelinesRecord) Draft() *PipelinesRecord {
	return &PipelinesRecord{
		Project: r.Project,
		ETag:    r.ETag,
		SHA:     store.Carry(r.SHA),
		Build:   store.Carry(r.Build),
		N:       store.Carry(r.N),
		Default: store.Carry(r.Default),
		At:      store.Carry(r.At),
		Defs:    store.Carry(r.Defs),
		Last:    store.Carry(r.Last),
	}
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
