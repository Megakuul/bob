package promote

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	settingsv1 "github.com/megakuul/bob/internal/gen/bob/settings/v1"
	"github.com/megakuul/bob/internal/gen/bob/settings/v1/settingsv1connect"
)

// SettingsService serves the signing-key RPCs of bob.settings.v1.SettingsService (R-REG-153).
// GetSettings and UpdateSettings belong to the settings milestone: a settings implementation
// embeds this type (or forwards to it) to keep the key RPCs.
type SettingsService struct {
	settingsv1connect.UnimplementedSettingsServiceHandler
	Keys *Keys
	// Retired is called after RetireSigningKey moved a key to RETIRING, to start the
	// re-sign sweep at once (nil: the next promotion pass does it).
	Retired func()
}

var keyStates = map[string]settingsv1.KeyState{
	KeyActive:     settingsv1.KeyState_KEY_STATE_ACTIVE,
	KeyVerifyOnly: settingsv1.KeyState_KEY_STATE_VERIFY_ONLY,
	KeyRetiring:   settingsv1.KeyState_KEY_STATE_RETIRING,
	KeyRetired:    settingsv1.KeyState_KEY_STATE_RETIRED,
}

func keyMessage(e KeyEntry) *settingsv1.SigningKey {
	return &settingsv1.SigningKey{Name: e.Name, PublicKey: e.Public, State: keyStates[e.State], CreatedAt: timestamppb.New(e.Created)}
}

func storageErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return api.Unavailable("signing keys are not available, try again")
}

// ListSigningKeys lists the keys clients trust (RETIRED keys are left out), ACTIVE first.
func (s *SettingsService) ListSigningKeys(ctx context.Context, _ *connect.Request[settingsv1.ListSigningKeysRequest]) (*connect.Response[settingsv1.ListSigningKeysResponse], error) {
	x, err := s.Keys.Index(ctx)
	if err != nil {
		return nil, storageErr(err)
	}
	out := &settingsv1.ListSigningKeysResponse{}
	if a := x.Active(); a != nil {
		out.Keys = append(out.Keys, keyMessage(*a))
	}
	for _, e := range x.Keys {
		if e.State != KeyActive && e.State != KeyRetired {
			out.Keys = append(out.Keys, keyMessage(e))
		}
	}
	return connect.NewResponse(out), nil
}

// CreateSigningKey adds a new ACTIVE key; the previous one becomes VERIFY_ONLY.
func (s *SettingsService) CreateSigningKey(ctx context.Context, _ *connect.Request[settingsv1.CreateSigningKeyRequest]) (*connect.Response[settingsv1.CreateSigningKeyResponse], error) {
	e, err := s.Keys.Create(ctx)
	if err != nil {
		return nil, storageErr(err)
	}
	return connect.NewResponse(&settingsv1.CreateSigningKeyResponse{Key: keyMessage(e)}), nil
}

// RetireSigningKey marks a VERIFY_ONLY key RETIRING; the re-sign sweep then makes it RETIRED.
func (s *SettingsService) RetireSigningKey(ctx context.Context, req *connect.Request[settingsv1.RetireSigningKeyRequest]) (*connect.Response[settingsv1.RetireSigningKeyResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, api.InvalidArgument("name", "name is required")
	}
	e, err := s.Keys.SetState(ctx, name, KeyRetiring, KeyVerifyOnly)
	switch {
	case errors.Is(err, ErrKeyNotFound):
		return nil, api.NotFound("signing key")
	case errors.Is(err, ErrKeyActive):
		return nil, api.FailedPrecondition(ErrKeyActive.Error())
	case err != nil:
		return nil, storageErr(err)
	}
	if s.Retired != nil && e.State == KeyRetiring {
		s.Retired()
	}
	return connect.NewResponse(&settingsv1.RetireSigningKeyResponse{Key: keyMessage(e)}), nil
}
