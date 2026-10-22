package rbac

import (
	"context"
	"errors"
	"slices"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
)

// The admin invariant of SPEC §6.4.1: at least one enabled principal holds admin@*. The
// AdminIndex is changed by CAS before a removal (leave) and after an addition (join), so its
// size never exceeds the real number of admins and two concurrent removals of the last two
// admins cannot both pass: the second CAS sees one member and refuses.

// errLastAdmin is the answer to a change that would leave no admin.
func errLastAdmin() error { return api.FailedPrecondition("at least one admin must remain") }

// isAdminBinding reports whether a binding makes its principal an admin.
func isAdminBinding(role, project string) bool { return role == Admin && project == Any }

// join adds principal to the admin index.
func (s *Service) join(ctx context.Context, principal string) error {
	_, err := store.CAS(ctx, s.control, store.AdminIndexKey(), func(cur *store.AdminIndex) (*store.AdminIndex, error) {
		if cur == nil {
			cur = store.AdminIndexKey()
		}
		i, found := slices.BinarySearch(cur.Principals, principal)
		if found {
			return nil, store.ErrNoChange
		}
		cur.Principals = slices.Insert(cur.Principals, i, principal)
		return cur, nil
	})
	return err
}

// leave removes principal from the admin index, refusing when it is the last admin. A
// principal missing from the index may leave while the index names another admin.
func (s *Service) leave(ctx context.Context, principal string) error {
	_, err := store.CAS(ctx, s.control, store.AdminIndexKey(), func(cur *store.AdminIndex) (*store.AdminIndex, error) {
		if cur == nil {
			cur = store.AdminIndexKey()
		}
		i, found := slices.BinarySearch(cur.Principals, principal)
		switch {
		case found && len(cur.Principals) == 1, !found && len(cur.Principals) == 0:
			return nil, errLastAdmin()
		case !found:
			return nil, store.ErrNoChange
		}
		cur.Principals = slices.Delete(cur.Principals, i, i+1)
		return cur, nil
	})
	return err
}

// rejoin undoes a leave after the write it protected failed; the original error is kept.
func (s *Service) rejoin(ctx context.Context, principal string, cause error) error {
	if err := s.join(context.WithoutCancel(ctx), principal); err != nil {
		s.log.ErrorContext(ctx, "rbac: admin index lost a member; it is repaired when the principal changes again",
			"principal", principal, "err", err.Error())
	}
	return cause
}

// hasAdmin reports whether bindings contain admin@*.
func hasAdmin(bs []store.RoleBinding) bool {
	return slices.ContainsFunc(bs, func(b store.RoleBinding) bool { return isAdminBinding(b.Role, b.Project) })
}

// BeforeDisable must be called by identity before it disables or deletes a principal (user or
// service account): when the principal holds admin@*, it leaves the admin index, refusing with
// FailedPrecondition "at least one admin must remain" for the last admin. If the disable or
// delete then fails, the caller runs undo.
func (s *Service) BeforeDisable(ctx context.Context, principal string) (undo func(context.Context), err error) {
	noop := func(context.Context) {}
	if s.principals == nil {
		return noop, nil
	}
	bs, enabled, err := s.principals.Bindings(ctx, principal)
	switch {
	case errors.Is(err, ErrNoPrincipal):
		return noop, nil
	case err != nil:
		return noop, err
	case !enabled || !hasAdmin(bs):
		return noop, nil
	}
	if err := s.leave(ctx, principal); err != nil {
		return noop, err
	}
	return func(ctx context.Context) { _ = s.rejoin(ctx, principal, nil) }, nil
}

// AfterEnable must be called by identity after it enabled a principal again: an admin@*
// binding counts again.
func (s *Service) AfterEnable(ctx context.Context, principal string) error {
	if s.principals == nil {
		return nil
	}
	bs, enabled, err := s.principals.Bindings(ctx, principal)
	if err != nil || !enabled || !hasAdmin(bs) {
		if errors.Is(err, ErrNoPrincipal) {
			return nil
		}
		return err
	}
	return s.join(ctx, principal)
}

// AfterDelete must be called by identity after it deleted a principal whose record held
// bindings: the derived role member lists drop them.
func (s *Service) AfterDelete(ctx context.Context, principal string, bindings []store.RoleBinding) error {
	var errs []error
	for _, b := range bindings {
		errs = append(errs, s.dropMember(ctx, b.Role, b.ID))
	}
	return errors.Join(errs...)
}
