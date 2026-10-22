package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
)

// BootstrapLogin is the login of the user behind the test stacks' bootstrap token.
const BootstrapLogin = "bootstrap-admin"

// Bootstrap makes token (BOB_ADMIN_TOKEN, honoured only by -tags bobtest builds) the API
// credential of a real local user "bootstrap-admin" bound to admin@* (R-AUTH-003: the admin
// counts in AdminIndex and shows in ListRoleBindings). The user is created once; every
// orchestrator of the stack finds the same user. The token itself is never stored.
func (id *Identity) Bootstrap(ctx context.Context, token string) error {
	var uid string
	for range 3 {
		u, err := id.lookupLogin(ctx, BootstrapLogin)
		if err != nil {
			return err
		}
		if u != nil {
			uid = u.ID
			break
		}
		uid, err = id.CreateLocalUser(ctx, NewLocalUser{
			Login: BootstrapLogin, DisplayName: "bootstrap admin",
			Password: newToken("bootstrap-"), Admin: true,
		})
		var ce *connect.Error
		if err == nil || !errors.As(err, &ce) || ce.Code() != connect.CodeAlreadyExists {
			break
		}
	}
	if uid == "" {
		return fmt.Errorf("the bootstrap admin user could not be created")
	}
	principal := "user:" + uid
	if _, _, err := id.updateBindings(ctx, principal, func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error) {
		if hasBinding(ro, adminRole, "*") {
			return ro, nil
		}
		return append(ro, store.RoleBindingEntry{ID: bindingID(), Role: adminRole, Project: "*", Source: sourceManual, Created: id.now()}), nil
	}); err != nil {
		return err
	}
	// Idempotent repairs (a crash between the steps of an earlier start).
	if err := id.adminAdd(ctx, principal); err != nil {
		return err
	}
	id.addMember(ctx, adminRole, principal)
	id.bootToken, id.bootUser = token, uid
	return nil
}

// bootstrapPrincipal authenticates the bootstrap token; ok is false for any other credential.
func (id *Identity) bootstrapPrincipal(ctx context.Context, bearer string) (p *api.Principal, ok bool, err error) {
	if id.bootToken == "" || subtle.ConstantTimeCompare([]byte(bearer), []byte(id.bootToken)) != 1 {
		return nil, false, nil
	}
	u, err := id.user(ctx, id.bootUser)
	if errors.Is(err, store.ErrNotFound) || err == nil && u.Disabled {
		return nil, true, errInvalidToken
	} else if err != nil {
		return nil, true, storageError(err)
	}
	p = userPrincipal(u)
	p.Scope = "api"
	return p, true, nil
}
