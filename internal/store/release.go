package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/megakuul/dynamitedb"
)

// Release records of the control bucket (SPEC §05.4.2, §05.6.4, §06 §6.8): the release
// record rel/<projectID>/n/<num10>, the uniqueness record relv, the per-project pages rpage
// and the registry-wide index ridx/ridxm. Release numbers come from pctr.r (ProjectCounters).

// Release states (§05.6.4).
const (
	ReleasePending    = "pending"
	ReleasePublishing = "publishing"
	ReleasePublished  = "published"
	ReleaseFailed     = "failed"
	ReleaseDeleted    = "deleted"
)

// MaxReleasePaths bounds the bob-served closure of one release kept in its record (the
// visibility sweeps of UpdateRelease and DeleteRelease walk it).
const MaxReleasePaths = 20_000

// ReleaseClosure is one platform's installable roots of a release.
type ReleaseClosure struct {
	Platform string   `cbor:"p,omitempty"`
	Roots    []string `cbor:"r,omitempty"`
	NarBytes int64    `cbor:"nb,omitempty"` // bob-served part only
}

// ReleaseAsset is one downloadable file of a release (rel/<projectID>/<num10>/<file>).
type ReleaseAsset struct {
	File     string `cbor:"f,omitempty"`
	Kind     string `cbor:"k,omitempty"` // appimage | tarball | file
	Platform string `cbor:"p,omitempty"`
	Size     int64  `cbor:"s,omitempty"`
	SHA256   string `cbor:"h,omitempty"`
}

// ReleaseImage is one OCI tag of a release.
type ReleaseImage struct {
	Repo      string `cbor:"r,omitempty"`
	Tag       string `cbor:"t,omitempty"`
	Digest    string `cbor:"d,omitempty"`
	MediaType string `cbor:"m,omitempty"`
}

// ReleaseRecord is rel/<projectID>/n/<num10> (§05.6.4). Published releases are immutable
// apart from visibility and description; retention never selects one (R-STORE-150).
type ReleaseRecord struct {
	Project dynamitedb.KeyField  `pk:"rel" cbor:"-"`
	N       dynamitedb.KeyField  `sk:"n" cbor:"-"`
	ETag    dynamitedb.ETagField `etag:"true" cbor:"-"`

	Name     dynamitedb.DataField[string] `cbor:"nm,omitempty"`
	Version  dynamitedb.DataField[string] `cbor:"v,omitempty"`
	Label    dynamitedb.DataField[string] `cbor:"lb,omitempty"`
	State    dynamitedb.DataField[string] `cbor:"st,omitempty"`
	Public   dynamitedb.DataField[bool]   `cbor:"pu,omitempty"`
	Build    dynamitedb.DataField[string] `cbor:"b,omitempty"`
	Pipeline dynamitedb.DataField[string] `cbor:"pi,omitempty"`
	SHA      dynamitedb.DataField[string] `cbor:"sha,omitempty"`
	Manifest dynamitedb.DataField[string] `cbor:"mf,omitempty"` // store path holding release.json

	Closures dynamitedb.DataField[[]ReleaseClosure] `cbor:"cl,omitempty"`
	Assets   dynamitedb.DataField[[]ReleaseAsset]   `cbor:"as,omitempty"`
	Images   dynamitedb.DataField[[]ReleaseImage]   `cbor:"im,omitempty"`
	// Paths are the store hashes of the bob-served closure, Blobs the sha256 hex digests of
	// the image objects: the visibility documents that list this release when it is public.
	Paths dynamitedb.DataField[[]string] `cbor:"ph,omitempty"`
	Blobs dynamitedb.DataField[[]string] `cbor:"ob,omitempty"`
	// Staged are the staging keys of the assets (until they are copied) and of the
	// COMPLETE.json files to delete after the copy.
	Staged dynamitedb.DataField[[]string] `cbor:"sg,omitempty"`

	Description dynamitedb.DataField[string]    `cbor:"ds,omitempty"`
	Error       dynamitedb.DataField[string]    `cbor:"er,omitempty"`
	Created     dynamitedb.DataField[time.Time] `cbor:"cr,omitempty"`
	Slot        dynamitedb.DataField[uint64]    `cbor:"ix,omitempty"` // ridx entry + 1 (0 = not indexed yet)
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ReleaseRecord) Draft() *ReleaseRecord {
	return &ReleaseRecord{
		Project: r.Project, N: r.N, ETag: r.ETag,
		Name: Carry(r.Name), Version: Carry(r.Version), Label: Carry(r.Label), State: Carry(r.State),
		Public: Carry(r.Public), Build: Carry(r.Build), Pipeline: Carry(r.Pipeline), SHA: Carry(r.SHA),
		Manifest: Carry(r.Manifest), Closures: Carry(r.Closures), Assets: Carry(r.Assets), Images: Carry(r.Images),
		Paths: Carry(r.Paths), Blobs: Carry(r.Blobs), Staged: Carry(r.Staged), Description: Carry(r.Description),
		Error: Carry(r.Error), Created: Carry(r.Created), Slot: Carry(r.Slot),
	}
}

// ReleaseRecordKey is the key value of release n of projectID.
func ReleaseRecordKey(projectID string, n uint64) *ReleaseRecord {
	num, _ := Num10(n)
	return &ReleaseRecord{Project: dynamitedb.Key(projectID), N: dynamitedb.Key(num)}
}

// ReleaseVersion is relv/<projectID.enc(name)>/v/<enc(version)>: the uniqueness of a release
// name and version within a project (create-only). It stays after DeleteRelease as a
// tombstone, so a version is never reused.
type ReleaseVersion struct {
	Scope   dynamitedb.KeyField          `pk:"relv" cbor:"-"`
	Version dynamitedb.KeyField          `sk:"v" cbor:"-"`
	ETag    dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N       dynamitedb.DataField[uint64] `cbor:"n,omitempty"` // 0 until the number is taken
	Build   dynamitedb.DataField[string] `cbor:"b,omitempty"` // the build that created it
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ReleaseVersion) Draft() *ReleaseVersion {
	return &ReleaseVersion{Scope: r.Scope, Version: r.Version, ETag: r.ETag, N: Carry(r.N), Build: Carry(r.Build)}
}

// ReleaseVersionKey is the key value of relv for (projectID, name, version).
func ReleaseVersionKey(projectID, name, version string) (*ReleaseVersion, error) {
	scope, err := Scoped(projectID, name)
	if err != nil {
		return nil, err
	}
	v, _ := Enc(version)
	if _, err := Segment(v); err != nil {
		return nil, err
	}
	return &ReleaseVersion{Scope: dynamitedb.Key(scope), Version: dynamitedb.Key(v)}, nil
}

// ReleaseEntry is one entry of a project's release pages (entry n-1 is release n).
type ReleaseEntry struct {
	N       uint64    `cbor:"n,omitempty"`
	Name    string    `cbor:"nm,omitempty"`
	Version string    `cbor:"v,omitempty"`
	State   string    `cbor:"st,omitempty"`
	Public  bool      `cbor:"pu,omitempty"`
	Created time.Time `cbor:"cr,omitzero"`
	Repos   []string  `cbor:"rp,omitempty"` // OCI repositories of the release's images
}

// ReleasePage is one page rpage/<projectID>/p/<num10> (P = 50) of a project's releases.
type ReleasePage struct {
	Project dynamitedb.KeyField                  `pk:"rpage" cbor:"-"`
	Page    dynamitedb.KeyField                  `sk:"p" cbor:"-"`
	ETag    dynamitedb.ETagField                 `etag:"true" cbor:"-"`
	Entries dynamitedb.DataField[[]ReleaseEntry] `cbor:"e,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ReleasePage) Draft() *ReleasePage {
	return &ReleasePage{Project: r.Project, Page: r.Page, ETag: r.ETag, Entries: Carry(r.Entries)}
}

// ReleasePageSize is P of rpage and ridx.
const ReleasePageSize = 50

// ReleasePages is the release list of a project; entry n-1 is release n.
func ReleasePages(control *Bucket, projectID string) *PagedIndex[ReleaseEntry] {
	return NewPagedIndex(control, ReleasePageSize, func(k uint64, e []ReleaseEntry) *ReleasePage {
		n, _ := Num10(k)
		return &ReleasePage{Project: dynamitedb.Key(projectID), Page: dynamitedb.Key(n), Entries: setNZ(e)}
	}, func(p *ReleasePage) []ReleaseEntry { return p.Entries.Value() })
}

// ReleaseIndexEntry is one entry of the registry-wide release index.
type ReleaseIndexEntry struct {
	Project string `cbor:"p,omitempty"`
	ReleaseEntry
}

// ReleaseIndexPage is one page ridx/all/p/<num10> (P = 50) of the registry-wide index.
type ReleaseIndexPage struct {
	Name    dynamitedb.KeyField                       `pk:"ridx" cbor:"-"` // "all"
	Page    dynamitedb.KeyField                       `sk:"p" cbor:"-"`
	ETag    dynamitedb.ETagField                      `etag:"true" cbor:"-"`
	Entries dynamitedb.DataField[[]ReleaseIndexEntry] `cbor:"e,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ReleaseIndexPage) Draft() *ReleaseIndexPage {
	return &ReleaseIndexPage{Name: r.Name, Page: r.Page, ETag: r.ETag, Entries: Carry(r.Entries)}
}

// ReleaseIndexCounter is ridxm/all: the number of ridx slots taken.
type ReleaseIndexCounter struct {
	Name dynamitedb.KeyField          `pk:"ridxm" cbor:"-"` // "all"
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N    dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ReleaseIndexCounter) Draft() *ReleaseIndexCounter {
	return &ReleaseIndexCounter{Name: r.Name, ETag: r.ETag, N: Carry(r.N)}
}

// ReleaseIndex is the registry-wide release index ridx/all (entry k is slot k+1).
func ReleaseIndex(control *Bucket) *PagedIndex[ReleaseIndexEntry] {
	return NewPagedIndex(control, ReleasePageSize, func(k uint64, e []ReleaseIndexEntry) *ReleaseIndexPage {
		n, _ := Num10(k)
		return &ReleaseIndexPage{Name: dynamitedb.Key("all"), Page: dynamitedb.Key(n), Entries: setNZ(e)}
	}, func(p *ReleaseIndexPage) []ReleaseIndexEntry { return p.Entries.Value() })
}

// ReleaseIndexSlots returns the number of ridx slots taken (ridxm/all).
func ReleaseIndexSlots(ctx context.Context, control *Bucket) (uint64, error) {
	c, err := Get(ctx, control, &ReleaseIndexCounter{Name: dynamitedb.Key("all")})
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return c.N.Value(), nil
}

// TakeReleaseIndexSlot raises ridxm/all by one (CAS) and returns the new count: the slot of a
// new ridx entry is count-1.
func TakeReleaseIndexSlot(ctx context.Context, control *Bucket) (uint64, error) {
	c, err := CAS(ctx, control, &ReleaseIndexCounter{Name: dynamitedb.Key("all")}, func(cur *ReleaseIndexCounter) (*ReleaseIndexCounter, error) {
		n := uint64(0)
		if cur != nil {
			n = cur.N.Value()
		}
		return &ReleaseIndexCounter{Name: dynamitedb.Key("all"), N: dynamitedb.Set(n + 1)}, nil
	})
	if err != nil {
		return 0, err
	}
	return c.N.Value(), nil
}

// ProjectReleases returns the release entries of a project (oldest first; entries of numbers
// that were taken but never written are left out): pctr + every rpage page.
func ProjectReleases(ctx context.Context, control *Bucket, projectID string) ([]ReleaseEntry, error) {
	ctr, err := Get(ctx, control, &ProjectCounters{Project: dynamitedb.Key(projectID)})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	total := ctr.Releases.Value()
	if total == 0 {
		return nil, nil
	}
	pages := ReleasePages(control, projectID)
	var out []ReleaseEntry
	for k := uint64(0); k <= pages.LastPage(total); k++ {
		es, err := pages.Page(ctx, k)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rpage/%s: %w", projectID, err)
		}
		for _, e := range es {
			if e.N != 0 {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// HasLiveReleases reports whether a project has a release that is not deleted (DeleteProject
// is refused while it has one, R-STORE-007). A number taken by a release whose page entry is
// not written yet counts as live: the release controller may be creating it.
func HasLiveReleases(ctx context.Context, control *Bucket, projectID string) (bool, error) {
	ctr, err := Get(ctx, control, &ProjectCounters{Project: dynamitedb.Key(projectID)})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	es, err := ProjectReleases(ctx, control, projectID)
	if err != nil {
		return false, err
	}
	if uint64(len(es)) < ctr.Releases.Value() {
		// a gap: either a crashed creation (no record) or one in progress (record without entry)
		seen := map[uint64]bool{}
		for _, e := range es {
			seen[e.N] = true
		}
		for n := uint64(1); n <= ctr.Releases.Value(); n++ {
			if seen[n] {
				continue
			}
			rec, err := Get(ctx, control, ReleaseRecordKey(projectID, n))
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return false, err
			}
			if rec.State.Value() != ReleaseDeleted {
				return true, nil
			}
		}
	}
	for _, e := range es {
		if e.State != ReleaseDeleted {
			return true, nil
		}
	}
	return false, nil
}

// OciHmacKey is ocihmac/<kid>: one HMAC key of the OCI token endpoint (32 bytes). Stored
// unsealed in the control bucket, which workers cannot read (like the signing-key seeds,
// D-IB9-2).
type OciHmacKey struct {
	Kid      dynamitedb.KeyField             `pk:"ocihmac" cbor:"-"`
	ETag     dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Key      dynamitedb.DataField[[]byte]    `cbor:"k,omitempty"`
	Created  dynamitedb.DataField[time.Time] `cbor:"cr,omitempty"`
	NotAfter dynamitedb.DataField[time.Time] `cbor:"na,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OciHmacKey) Draft() *OciHmacKey {
	return &OciHmacKey{Kid: r.Kid, ETag: r.ETag, Key: Carry(r.Key), Created: Carry(r.Created), NotAfter: Carry(r.NotAfter)}
}

// OciHmacEntry is one key of the index ocihmacs/all.
type OciHmacEntry struct {
	Kid      string    `cbor:"id,omitempty"`
	Created  time.Time `cbor:"cr,omitzero"`
	NotAfter time.Time `cbor:"na,omitzero"`
}

// OciHmacKeys is ocihmacs/all: the current keys, newest last.
type OciHmacKeys struct {
	Name dynamitedb.KeyField                  `pk:"ocihmacs" cbor:"-"` // "all"
	ETag dynamitedb.ETagField                 `etag:"true" cbor:"-"`
	Keys dynamitedb.DataField[[]OciHmacEntry] `cbor:"k,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OciHmacKeys) Draft() *OciHmacKeys {
	return &OciHmacKeys{Name: r.Name, ETag: r.ETag, Keys: Carry(r.Keys)}
}
