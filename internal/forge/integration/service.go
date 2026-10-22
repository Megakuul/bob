package integration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/forge"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	"github.com/megakuul/bob/internal/gen/bob/forge/v1/forgev1connect"
	"github.com/megakuul/bob/internal/store"
)

// Service implements ForgeService (SPEC §6.5.14).
type Service struct {
	Registry *Registry
	BaseURL  string // server.base_url: webhook URLs are <BaseURL>/hooks/<id>
}

var _ forgev1connect.ForgeServiceHandler = (*Service)(nil)

// prevSecretFor is how long the previous webhook secret is accepted after a rotation.
const prevSecretFor = 24 * time.Hour

// ListDeliveries is the number of ring entries ListForgeDeliveries returns (newest first).
const ListDeliveries = 100

var idRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

func storageErr(err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, store.ErrConflict):
		return api.Aborted("the integration changed concurrently, try again")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return api.Unavailable("storage unavailable")
	}
	return err
}

func kindOf(k forgev1.ForgeKind) (forge.Kind, bool) {
	switch k {
	case forgev1.ForgeKind_FORGE_KIND_GITHUB:
		return forge.GitHub, true
	case forgev1.ForgeKind_FORGE_KIND_FORGEJO:
		return forge.Forgejo, true
	case forgev1.ForgeKind_FORGE_KIND_GITLAB:
		return forge.GitLab, true
	}
	return "", false
}

func kindMsg(k string) forgev1.ForgeKind {
	switch forge.Kind(k) {
	case forge.GitHub:
		return forgev1.ForgeKind_FORGE_KIND_GITHUB
	case forge.Forgejo:
		return forgev1.ForgeKind_FORGE_KIND_FORGEJO
	case forge.GitLab:
		return forgev1.ForgeKind_FORGE_KIND_GITLAB
	}
	return forgev1.ForgeKind_FORGE_KIND_UNSPECIFIED
}

// msg is the API view of rec: credentials without their secret parts.
func (s *Service) msg(rec *ForgeRecord) *forgev1.ForgeIntegration {
	m := &forgev1.ForgeIntegration{Id: rec.ID, Kind: kindMsg(rec.Kind), DisplayName: rec.DisplayName, BaseUrl: rec.BaseURL,
		WebhookUrl: HookURL(s.BaseURL, rec.ID), Health: forgev1.Health(rec.Health), HealthMessage: rec.HealthMsg,
		CreatedAt: timestamppb.New(rec.Created), Etag: rec.ETag}
	switch forge.Kind(rec.Kind) {
	case forge.GitHub:
		m.Credentials = &forgev1.ForgeIntegration_Github{Github: &forgev1.GitHubCredentials{AppId: rec.AppID, InstallationId: rec.InstallationID}}
		m.HasCredentials = rec.PrivateKeyPEM != ""
	case forge.Forgejo:
		m.Credentials = &forgev1.ForgeIntegration_Forgejo{Forgejo: &forgev1.ForgejoCredentials{BotLogin: rec.BotLogin}}
		m.HasCredentials = rec.BotPassword != ""
	case forge.GitLab:
		m.Credentials = &forgev1.ForgeIntegration_Gitlab{Gitlab: &forgev1.GitLabCredentials{AllowPlaintextSecret: rec.AllowPlaintext}}
		m.HasCredentials = rec.AccessToken != ""
	}
	return m
}

// setCredentials copies the request's credentials into rec; a credential of another kind is
// refused. Empty secret fields keep the stored value (they are input only).
func setCredentials(rec *ForgeRecord, in *forgev1.ForgeIntegration) error {
	switch c := in.GetCredentials().(type) {
	case nil:
	case *forgev1.ForgeIntegration_Github:
		if rec.Kind != string(forge.GitHub) {
			return api.InvalidArgument("integration.github", "GitHub credentials on a "+rec.Kind+" integration")
		}
		rec.AppID, rec.InstallationID = c.Github.GetAppId(), c.Github.GetInstallationId()
		if c.Github.GetPrivateKeyPem() != "" {
			rec.PrivateKeyPEM = c.Github.GetPrivateKeyPem()
		}
	case *forgev1.ForgeIntegration_Forgejo:
		if rec.Kind != string(forge.Forgejo) {
			return api.InvalidArgument("integration.forgejo", "Forgejo credentials on a "+rec.Kind+" integration")
		}
		rec.BotLogin = c.Forgejo.GetBotLogin()
		if c.Forgejo.GetBotPassword() != "" {
			rec.BotPassword = c.Forgejo.GetBotPassword()
		}
	case *forgev1.ForgeIntegration_Gitlab:
		if rec.Kind != string(forge.GitLab) {
			return api.InvalidArgument("integration.gitlab", "GitLab credentials on a "+rec.Kind+" integration")
		}
		rec.AllowPlaintext = c.Gitlab.GetAllowPlaintextSecret()
		if c.Gitlab.GetAccessToken() != "" {
			rec.AccessToken = c.Gitlab.GetAccessToken()
		}
	}
	return nil
}

// validate builds the adapter of rec: the constructors check the base URL and the shape of
// the credentials.
func validate(rec *ForgeRecord) error {
	if len(rec.DisplayName) > 100 {
		return api.InvalidArgument("integration.displayName", "display name longer than 100 characters")
	}
	if _, err := NewAdapter(rec, rec.Secret); err != nil {
		return api.InvalidArgument("integration", err.Error())
	}
	return nil
}

// CreateForgeIntegration creates the integration and returns its webhook secret once.
func (s *Service) CreateForgeIntegration(ctx context.Context, req *connect.Request[forgev1.CreateForgeIntegrationRequest]) (*connect.Response[forgev1.CreateForgeIntegrationResponse], error) {
	in := req.Msg.GetIntegration()
	if !idRE.MatchString(in.GetId()) {
		return nil, api.InvalidArgument("integration.id", "an integration id must match ^[a-z][a-z0-9-]{1,38}[a-z0-9]$")
	}
	kind, ok := kindOf(in.GetKind())
	if !ok {
		return nil, api.InvalidArgument("integration.kind", "kind must be GitHub, Forgejo or GitLab")
	}
	secret, err := NewSecret(kind)
	if err != nil {
		return nil, err
	}
	rec := ForgeKey(in.GetId())
	rec.Kind, rec.DisplayName, rec.BaseURL, rec.Secret, rec.Created = string(kind), in.GetDisplayName(), in.GetBaseUrl(), secret, s.Registry.now().UTC()
	if kind == forge.GitHub && rec.BaseURL == "" {
		rec.BaseURL = GitHubAPI
	}
	if err := setCredentials(rec, in); err != nil {
		return nil, err
	}
	if err := validate(rec); err != nil {
		return nil, err
	}
	// Index first (intent-first, R-SRV-101): readers skip ids without a record.
	if _, err := store.CAS(ctx, s.Registry.Control, ForgeIndexKey(), func(cur *ForgeIndex) (*ForgeIndex, error) {
		if cur == nil {
			cur = ForgeIndexKey()
		}
		if slices.Contains(cur.IDs, rec.ID) {
			return nil, store.ErrNoChange
		}
		if len(cur.IDs) >= MaxIntegrations {
			return nil, api.FailedPrecondition(fmt.Sprintf("at most %d forge integrations", MaxIntegrations))
		}
		cur.IDs = append(cur.IDs, rec.ID)
		slices.Sort(cur.IDs)
		return cur, nil
	}); err != nil {
		return nil, storageErr(err)
	}
	if err := store.Create(ctx, s.Registry.Control, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, api.AlreadyExists(fmt.Sprintf("forge integration %s already exists", rec.ID))
		}
		return nil, storageErr(err)
	}
	return connect.NewResponse(&forgev1.CreateForgeIntegrationResponse{Integration: s.msg(rec), WebhookSecret: secret}), nil
}

func (s *Service) get(ctx context.Context, id string) (*ForgeRecord, error) {
	if _, err := store.Segment(id); err != nil {
		return nil, api.NotFound("forge integration")
	}
	rec, err := store.Get(ctx, s.Registry.Control, ForgeKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("forge integration")
	}
	return rec, storageErr(err)
}

// GetForgeIntegration returns one integration.
func (s *Service) GetForgeIntegration(ctx context.Context, req *connect.Request[forgev1.GetForgeIntegrationRequest]) (*connect.Response[forgev1.GetForgeIntegrationResponse], error) {
	rec, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&forgev1.GetForgeIntegrationResponse{Integration: s.msg(rec)}), nil
}

// IDs returns the integration ids of forges/all.
func (r *Registry) IDs(ctx context.Context) ([]string, error) {
	idx, err := store.Get(ctx, r.Control, ForgeIndexKey())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return idx.IDs, nil
}

// ListForgeIntegrations lists all integrations (≤ 50).
func (s *Service) ListForgeIntegrations(ctx context.Context, _ *connect.Request[forgev1.ListForgeIntegrationsRequest]) (*connect.Response[forgev1.ListForgeIntegrationsResponse], error) {
	ids, err := s.Registry.IDs(ctx)
	if err != nil {
		return nil, storageErr(err)
	}
	out := &forgev1.ListForgeIntegrationsResponse{}
	for _, id := range ids {
		rec, err := store.Get(ctx, s.Registry.Control, ForgeKey(id))
		if errors.Is(err, store.ErrNotFound) {
			continue // intent-first index entry of an unfinished create
		}
		if err != nil {
			return nil, storageErr(err)
		}
		out.Integrations = append(out.Integrations, s.msg(rec))
	}
	return connect.NewResponse(out), nil
}

// UpdateForgeIntegration updates display_name, base_url and the credentials.
func (s *Service) UpdateForgeIntegration(ctx context.Context, req *connect.Request[forgev1.UpdateForgeIntegrationRequest]) (*connect.Response[forgev1.UpdateForgeIntegrationResponse], error) {
	in := req.Msg.GetIntegration()
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "display_name", "base_url", "github", "forgejo", "gitlab"); err != nil {
		return nil, err
	}
	if _, err := s.get(ctx, in.GetId()); err != nil {
		return nil, err
	}
	out, err := store.CAS(ctx, s.Registry.Control, ForgeKey(in.GetId()), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil {
			return nil, api.NotFound("forge integration")
		}
		if in.GetEtag() != "" && in.GetEtag() != cur.ETag {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		for _, p := range req.Msg.GetUpdateMask().GetPaths() {
			switch p {
			case "displayName", "display_name":
				cur.DisplayName = in.GetDisplayName()
			case "baseUrl", "base_url":
				cur.BaseURL = in.GetBaseUrl()
			default: // github | forgejo | gitlab
				if err := setCredentials(cur, in); err != nil {
					return nil, err
				}
				cur.Health, cur.HealthMsg = 0, ""
			}
		}
		if err := validate(cur); err != nil {
			return nil, err
		}
		return cur, nil
	})
	if err != nil {
		return nil, storageErr(err)
	}
	return connect.NewResponse(&forgev1.UpdateForgeIntegrationResponse{Integration: s.msg(out)}), nil
}

// DeleteForgeIntegration deletes an integration no project links to.
func (s *Service) DeleteForgeIntegration(ctx context.Context, req *connect.Request[forgev1.DeleteForgeIntegrationRequest]) (*connect.Response[forgev1.DeleteForgeIntegrationResponse], error) {
	rec, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetEtag() != "" && req.Msg.GetEtag() != rec.ETag {
		return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	if n := len(rec.Repos); n > 0 {
		return nil, api.FailedPrecondition(fmt.Sprintf("forge integration %s is linked to %d repositories; unlink the projects first", rec.ID, n))
	}
	if err := store.Delete(ctx, s.Registry.Control, rec); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, api.NotFound("forge integration")
		}
		return nil, storageErr(err)
	}
	if _, err := store.CAS(ctx, s.Registry.Control, ForgeIndexKey(), func(cur *ForgeIndex) (*ForgeIndex, error) {
		if cur == nil || !slices.Contains(cur.IDs, rec.ID) {
			return nil, store.ErrNoChange
		}
		cur.IDs = slices.DeleteFunc(cur.IDs, func(id string) bool { return id == rec.ID })
		return cur, nil
	}); err != nil {
		return nil, storageErr(err)
	}
	return connect.NewResponse(&forgev1.DeleteForgeIntegrationResponse{}), nil
}

// RotateWebhookSecret replaces the secret (the previous one is accepted for 24 h) and
// re-ensures the hooks of the linked repositories with the new one.
func (s *Service) RotateWebhookSecret(ctx context.Context, req *connect.Request[forgev1.RotateWebhookSecretRequest]) (*connect.Response[forgev1.RotateWebhookSecretResponse], error) {
	rec, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	secret, err := NewSecret(forge.Kind(rec.Kind))
	if err != nil {
		return nil, err
	}
	now := s.Registry.now().UTC()
	out, err := store.CAS(ctx, s.Registry.Control, ForgeKey(rec.ID), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil {
			return nil, api.NotFound("forge integration")
		}
		cur.PrevSecret, cur.PrevUntil, cur.Secret = cur.Secret, now.Add(prevSecretFor), secret
		return cur, nil
	})
	if err != nil {
		return nil, storageErr(err)
	}
	_, a, err := s.Registry.Adapter(ctx, rec.ID)
	if err != nil {
		return nil, storageErr(err)
	}
	var failed []string
	for rid := range out.Repos {
		var id int64
		if _, err := fmt.Sscan(rid, &id); err != nil {
			continue
		}
		link, err := s.Registry.Link(ctx, rec.ID, id)
		if err != nil {
			continue // intent-first entry without a link
		}
		repo := forge.Repo{Forge: forge.Kind(rec.Kind), ID: id, FullName: link.FullName, CloneURL: link.CloneURL}
		err = a.EnsureWebhook(ctx, repo, HookURL(s.BaseURL, rec.ID), forge.NewSecret(secret))
		s.Registry.Report(ctx, rec.ID, err)
		if err != nil && !errors.Is(err, forge.ErrUnsupported) {
			failed = append(failed, link.FullName)
		}
	}
	if len(failed) > 0 {
		s.Registry.log().Warn("forge: webhook not re-ensured after rotation; the previous secret works for 24 h",
			"integration", rec.ID, "repositories", failed)
	}
	return connect.NewResponse(&forgev1.RotateWebhookSecretResponse{WebhookSecret: secret}), nil
}

// TestForgeIntegration checks the credentials against the forge: with a repository by
// resolving it, otherwise with the forge's own credential check where one exists.
func (s *Service) TestForgeIntegration(ctx context.Context, req *connect.Request[forgev1.TestForgeIntegrationRequest]) (*connect.Response[forgev1.TestForgeIntegrationResponse], error) {
	rec, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	_, a, err := s.Registry.Adapter(ctx, rec.ID)
	if err != nil {
		return nil, storageErr(err)
	}
	out := &forgev1.TestForgeIntegrationResponse{}
	if name := req.Msg.GetRepoFullName(); name != "" {
		repo, err := a.LookupRepo(ctx, name)
		out.Health, out.Message = health(rec.ID, err)
		if err == nil {
			out.RepoId, out.Message = repo.ID, fmt.Sprintf("repository %s (id %d) is reachable", repo.FullName, repo.ID)
		}
		s.Registry.Report(ctx, rec.ID, err)
		return connect.NewResponse(out), nil
	}
	if forge.Kind(rec.Kind) != forge.Forgejo {
		out.Health, out.Message = forgev1.Health(rec.Health), "name a repository to check the credentials against the forge"
		return connect.NewResponse(out), nil
	}
	// Forgejo: listing the bot's tokens needs the bot password; nothing is older than year 1.
	_, err = a.SweepCheckouts(ctx, time.Time{})
	out.Health, out.Message = health(rec.ID, err)
	s.Registry.Report(ctx, rec.ID, err)
	return connect.NewResponse(out), nil
}

func health(id string, err error) (forgev1.Health, string) {
	switch {
	case err == nil:
		return forgev1.Health_HEALTH_OK, "credentials accepted"
	case errors.Is(err, forge.ErrAuth):
		return forgev1.Health_HEALTH_AUTH_FAILED, fmt.Sprintf("forge: integration %s: forge rejected bob's credentials (HTTP 401); re-enter the credentials in Settings → Forges", id)
	case errors.Is(err, forge.ErrNotFound):
		return forgev1.Health_HEALTH_OK, "repository not found or not visible to bob's credentials"
	}
	return forgev1.Health_HEALTH_UNREACHABLE, "forge unreachable: " + err.Error()
}

// ListForgeDeliveries returns the newest deliveries of the ring.
func (s *Service) ListForgeDeliveries(ctx context.Context, req *connect.Request[forgev1.ListForgeDeliveriesRequest]) (*connect.Response[forgev1.ListForgeDeliveriesResponse], error) {
	rec, err := s.get(ctx, req.Msg.GetIntegrationId())
	if err != nil {
		return nil, err
	}
	ring, err := store.Get(ctx, s.Registry.Control, &DeliveryRing{Forge: rec.ID})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, storageErr(err)
	}
	out := &forgev1.ListForgeDeliveriesResponse{}
	if ring != nil {
		for i := len(ring.Entries) - 1; i >= 0 && len(out.Deliveries) < ListDeliveries; i-- {
			e := ring.Entries[i]
			out.Deliveries = append(out.Deliveries, &forgev1.Delivery{DeliveryId: e.ID, Event: e.Event, Result: e.Result,
				BuildId: e.Build, ReceivedAt: timestamppb.New(e.Received)})
		}
	}
	return connect.NewResponse(out), nil
}
