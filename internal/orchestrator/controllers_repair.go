package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
)

// IntentGrace is the age after which an intent-first index entry whose record is missing is
// removed (R-SRV-101): its writer died between the two writes.
const IntentGrace = 5 * time.Minute

// BuildEntryTombstone is the state of a build page entry whose build record is gone (removed
// by index-repair or retention). Readers skip it; positions in the paged index never move.
const BuildEntryTombstone = "deleted"

// adminRole is the built-in role id (not stored in roles/all).
const adminRole = "admin"

// IndexRepair is the index-repair controller (10 min, R-SRV-101): it finishes intent-first
// entries older than IntentGrace whose record does not exist (jact, bpage, rmem) and adds
// missing role-member entries of existing bindings. Every write is a CAS on a known key; it
// never lists.
type IndexRepair struct {
	Control, Fleet *store.Bucket
	JobShards      int // fleet.job_shards
	// Projects returns every project id (the pidx walk of the project service); nil skips the
	// bpage repair.
	Projects func(ctx context.Context) ([]string, error)
	Log      *slog.Logger
}

func (x *IndexRepair) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.Default()
}

// repairState is the persisted state of index-repair: role-member entries without a binding,
// with the time they were first seen (rmem entries carry no time of their own).
type repairState struct {
	Suspect map[string]time.Time `cbor:"s,omitempty"` // "<role>\x00<principal>" -> first seen
}

// RepairResult counts what one pass changed.
type RepairResult struct {
	Jobs, Builds, MembersRemoved, MembersAdded int
}

// Pass runs one repair pass. Errors of one index do not stop the others.
func (x *IndexRepair) Pass(ctx context.Context, r *Run) error {
	_, err := x.Repair(ctx, r)
	return err
}

// Repair is Pass with its counts (tests).
func (x *IndexRepair) Repair(ctx context.Context, r *Run) (RepairResult, error) {
	var res RepairResult
	var st repairState
	if len(r.State) > 0 {
		_ = cbor.Unmarshal(r.State, &st) // damaged state: start over
	}
	var errs []error
	if x.Fleet != nil {
		n, err := x.jobs(ctx, r.Now)
		res.Jobs = n
		errs = append(errs, err)
	}
	if x.Control != nil && x.Projects != nil {
		n, err := x.builds(ctx, r.Now)
		res.Builds = n
		errs = append(errs, err)
	}
	if x.Control != nil {
		removed, added, err := x.members(ctx, r.Now, &st)
		res.MembersRemoved, res.MembersAdded = removed, added
		errs = append(errs, err)
	}
	if b, err := cbor.Marshal(st); err == nil {
		r.State = b
	}
	return res, errors.Join(errs...)
}

func old(created, now time.Time) bool { return created.IsZero() || now.Sub(created) > IntentGrace }

// jobs removes jact entries older than IntentGrace whose job record is missing.
func (x *IndexRepair) jobs(ctx context.Context, now time.Time) (int, error) {
	shards := x.JobShards
	if shards <= 0 {
		shards = 16
	}
	set := store.ActiveJobs(x.Fleet, shards)
	all, err := set.All(ctx)
	if err != nil {
		return 0, fmt.Errorf("index-repair jact: %w", err)
	}
	n := 0
	var errs []error
	for id, e := range all {
		if !old(e.Created, now) {
			continue
		}
		_, err := store.Get(ctx, x.Fleet, &store.JobRecord{ID: id})
		if !errors.Is(err, store.ErrNotFound) {
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := set.Remove(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
		x.log().Info("index-repair: removed jact entry without job record", "job", id, "build", e.Build)
	}
	return n, errors.Join(errs...)
}

// builds tombstones bpage entries older than IntentGrace whose build record is missing.
func (x *IndexRepair) builds(ctx context.Context, now time.Time) (int, error) {
	projects, err := x.Projects(ctx)
	if err != nil {
		return 0, fmt.Errorf("index-repair projects: %w", err)
	}
	n := 0
	var errs []error
	for _, p := range projects {
		idx := store.BuildPages(x.Control, p)
		for k := uint64(0); ; k++ {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			page, err := idx.Page(ctx, k)
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			if err != nil {
				errs = append(errs, err)
				break
			}
			for off, e := range page {
				if e.Build == "" || e.State == BuildEntryTombstone || !old(e.Created, now) {
					continue
				}
				_, err := store.Get(ctx, x.Control, &store.BuildRecord{ID: e.Build})
				if !errors.Is(err, store.ErrNotFound) {
					if err != nil {
						errs = append(errs, err)
					}
					continue
				}
				pos := k*uint64(idx.P) + uint64(off)
				if err := idx.Set(ctx, pos, store.BuildEntry{N: e.N, State: BuildEntryTombstone}); err != nil {
					errs = append(errs, err)
					continue
				}
				n++
				x.log().Info("index-repair: tombstoned bpage entry without build record", "project", p, "build", e.Build)
			}
		}
	}
	return n, errors.Join(errs...)
}

// principalBinds reports whether the principal ("user:<id>" | "sa:<id>") has a binding of
// role; exists is false when its record is gone.
func (x *IndexRepair) principalBinds(ctx context.Context, principal, role string) (binds, exists bool, err error) {
	var bs []store.RoleBindingEntry
	switch kind, id, _ := strings.Cut(principal, ":"); kind {
	case "user":
		u, err := store.Get(ctx, x.Control, store.UserKey(id))
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrKeySegment) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		bs = u.Bindings
	case "sa":
		sa, err := store.Get(ctx, x.Control, store.ServiceAccountKey(id))
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrKeySegment) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		bs = sa.Bindings
	default:
		return false, false, nil
	}
	return slices.ContainsFunc(bs, func(b store.RoleBindingEntry) bool { return b.Role == role }), true, nil
}

// members repairs rmem/<role>: an entry without a binding (seen on two passes at least
// IntentGrace apart) is removed; a binding without an entry is added.
func (x *IndexRepair) members(ctx context.Context, now time.Time, st *repairState) (removed, added int, err error) {
	roles := []string{adminRole}
	ri, err := store.Get(ctx, x.Control, store.RolesIndexKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, 0, fmt.Errorf("index-repair roles: %w", err)
	}
	if ri != nil {
		roles = append(roles, ri.IDs...)
	}
	seen := map[string]bool{}
	var errs []error
	for _, role := range roles {
		mem, err := store.Get(ctx, x.Control, store.RoleMembersKey(role))
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrKeySegment) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, p := range mem.Principals {
			binds, _, err := x.principalBinds(ctx, p, role)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if binds {
				continue
			}
			key := role + "\x00" + p
			seen[key] = true
			first, ok := st.Suspect[key]
			if !ok {
				if st.Suspect == nil {
					st.Suspect = map[string]time.Time{}
				}
				st.Suspect[key] = now
				continue
			}
			if now.Sub(first) < IntentGrace {
				continue
			}
			if err := x.dropMember(ctx, role, p); err != nil {
				errs = append(errs, err)
				continue
			}
			delete(st.Suspect, key)
			removed++
			x.log().Info("index-repair: removed role member without binding", "role", role, "principal", p)
		}
	}
	for k := range st.Suspect {
		if !seen[k] {
			delete(st.Suspect, k)
		}
	}
	a, err := x.addMissingMembers(ctx)
	errs = append(errs, err)
	return removed, a, errors.Join(errs...)
}

// dropMember removes principal from rmem/<role> after re-checking its record.
func (x *IndexRepair) dropMember(ctx context.Context, role, principal string) error {
	binds, _, err := x.principalBinds(ctx, principal, role)
	if err != nil || binds {
		return err
	}
	_, err = store.CAS(ctx, x.Control, store.RoleMembersKey(role), func(cur *store.RoleMembers) (*store.RoleMembers, error) {
		if cur == nil || !slices.Contains(cur.Principals, principal) {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Principals = slices.DeleteFunc(slices.Clone(cur.Principals), func(p string) bool { return p == principal })
		return &next, nil
	})
	return err
}

// addMissingMembers walks the users (uidx pages) and service accounts (saidx) and adds every
// bound principal that rmem/<role> lacks.
func (x *IndexRepair) addMissingMembers(ctx context.Context) (int, error) {
	want := map[string][]string{} // role -> principals
	var errs []error
	collect := func(principal string, bs []store.RoleBindingEntry) {
		for _, b := range bs {
			if b.Role != "" && !slices.Contains(want[b.Role], principal) {
				want[b.Role] = append(want[b.Role], principal)
			}
		}
	}
	users := store.UserIndex(x.Control)
	for k := uint64(0); ; k++ {
		page, err := users.Page(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			errs = append(errs, err)
			break
		}
		for _, e := range page {
			if e.ID == "" {
				continue
			}
			u, err := store.Get(ctx, x.Control, store.UserKey(e.ID))
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrKeySegment) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			collect("user:"+u.ID, u.Bindings)
		}
	}
	sas, err := store.Get(ctx, x.Control, store.SAIndexKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		errs = append(errs, err)
	}
	if sas != nil {
		for _, id := range sas.IDs {
			sa, err := store.Get(ctx, x.Control, store.ServiceAccountKey(id))
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrKeySegment) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			collect("sa:"+sa.ID, sa.Bindings)
		}
	}
	added := 0
	for role, ps := range want {
		var n int
		_, err := store.CAS(ctx, x.Control, store.RoleMembersKey(role), func(cur *store.RoleMembers) (*store.RoleMembers, error) {
			next := store.RoleMembersKey(role)
			if cur != nil {
				*next = *cur
			}
			n = 0
			members := slices.Clone(next.Principals)
			for _, p := range ps {
				if !slices.Contains(members, p) {
					members = append(members, p)
					n++
				}
			}
			if n == 0 {
				return nil, store.ErrNoChange
			}
			next.Principals = members
			return next, nil
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if n > 0 {
			added += n
			x.log().Info("index-repair: added missing role members", "role", role, "count", n)
		}
	}
	return added, errors.Join(errs...)
}

// Controller returns the index-repair controller (10 min, lease ctl/index-repair).
func (x *IndexRepair) Controller(owner string, skew time.Duration, now func() time.Time) *Controller {
	return &Controller{ID: CtlIndexRepair, Fleet: x.Fleet, Owner: owner, Interval: 10 * time.Minute, Skew: skew,
		Now: now, Persist: true, Pass: x.Pass, Log: x.Log}
}
