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
	m := &forgev1.ForgeIntegration{Id: rec.ID.Value(), Kind: kindMsg(rec.Kind.Value()), DisplayName: rec.DisplayName.Value(), BaseUrl: rec.BaseURL.Value(),
		WebhookUrl: HookURL(s.BaseURL, rec.ID.Value()), Health: forgev1.Health(rec.Health.Value()), HealthMessage: rec.HealthMsg.Value(),
		CreatedAt: timestamppb.New(rec.Created.Value()), Etag: store.ETagOf(rec.ETag)}
	switch forge.Kind(rec.Kind.Value()) {
	case forge.GitHub:
		m.Credentials = &forgev1.ForgeIntegration_Github{Github: &forgev1.GitHubCredentials{AppId: rec.AppID.Value(), InstallationId: rec.InstallationID.Value()}}
		m.HasCredentials = rec.PrivateKeyPEM.Value() != ""
	case forge.Forgejo:
		m.Credentials = &forgev1.ForgeIntegration_Forgejo{Forgejo: &forgev1.ForgejoCredentials{BotLogin: rec.BotLogin.Value()}}
		m.HasCredentials = rec.BotPassword.Value() != ""
	case forge.GitLab:
		m.Credentials = &forgev1.ForgeIntegration_Gitlab{Gitlab: &forgev1.GitLabCredentials{AllowPlaintextSecret: rec.AllowPlaintext.Value()}}
		m.HasCredentials = rec.AccessToken.Value() != ""
	}
	return m
}

// setCredentials copies the request's credentials into rec; a credential of another kind is
// refused. Empty secret fields keep the stored value (they are input only).
func setCredentials(rec *Config, in *forgev1.ForgeIntegration) error {
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

// validate builds the adapter of c: the constructors check the base URL and the shape of
// the credentials.
func validate(c Config, secret string) error {
	if len(c.DisplayName) > 100 {
		return api.InvalidArgument("integration.displayName", "display name longer than 100 characters")
	}
	if _, err := NewAdapter(c, secret); err != nil {
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
	c := Config{Kind: string(kind), DisplayName: in.GetDisplayName(), BaseURL: in.GetBaseUrl()}
	if kind == forge.GitHub && c.BaseURL == "" {
		c.BaseURL = GitHubAPI
	}
	if err := setCredentials(&c, in); err != nil {
		return nil, err
	}
	if err := validate(c, secret); err != nil {
		return nil, err
	}
	rec := ForgeKey(in.GetId())
	c.apply(rec)
	rec.Secret, rec.Created = store.Set(secret), store.Set(s.Registry.now().UTC())
	// Index first (intent-first, R-SRV-101): readers skip ids without a record.
	if err := store.Change(ctx, s.Registry.Control, ForgeIndexKey(), func(cur *ForgeIndex) (*ForgeIndex, error) {
		var vals []string
		if cur == nil {
			cur = ForgeIndexKey()
		} else {
			vals = cur.IDs.Value()
		}
		if slices.Contains(vals, rec.ID.Value()) {
			return nil, store.ErrNoChange
		}
		if len(vals) >= MaxIntegrations {
			return nil, api.FailedPrecondition(fmt.Sprintf("at most %d forge integrations", MaxIntegrations))
		}
		vals = append(vals, rec.ID.Value())
		slices.Sort(vals)
		cur.IDs = store.Set(vals)
		return cur, nil
	}); err != nil {
		return nil, storageErr(err)
	}
	if err := store.Create(ctx, s.Registry.Control, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, api.AlreadyExists(fmt.Sprintf("forge integration %s already exists", rec.ID.Value()))
		}
		return nil, storageErr(err)
	}
	created, err := s.get(ctx, in.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&forgev1.CreateForgeIntegrationResponse{Integration: s.msg(created), WebhookSecret: secret}), nil
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
	return idx.IDs.Value(), nil
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
		if in.GetEtag() != "" && in.GetEtag() != store.ETagOf(cur.ETag) {
			return nil, api.ETagMismatch()
		}
		c := ConfigOf(cur)
		for _, p := range req.Msg.GetUpdateMask().GetPaths() {
			switch p {
			case "displayName", "display_name":
				c.DisplayName = in.GetDisplayName()
			case "baseUrl", "base_url":
				c.BaseURL = in.GetBaseUrl()
			default: // github | forgejo | gitlab
				if err := setCredentials(&c, in); err != nil {
					return nil, err
				}
				cur.Health, cur.HealthMsg = nil, nil
			}
		}
		if err := validate(c, cur.Secret.Value()); err != nil {
			return nil, err
		}
		c.apply(cur)
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
	if req.Msg.GetEtag() != "" && req.Msg.GetEtag() != store.ETagOf(rec.ETag) {
		return nil, api.ETagMismatch()
	}
	if n := len(rec.Repos.Value()); n > 0 {
		return nil, api.FailedPrecondition(fmt.Sprintf("forge integration %s is linked to %d repositories; unlink the projects first", rec.ID.Value(), n))
	}
	if err := store.Delete(ctx, s.Registry.Control, rec); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, api.NotFound("forge integration")
		}
		return nil, storageErr(err)
	}
	if err := store.Change(ctx, s.Registry.Control, ForgeIndexKey(), func(cur *ForgeIndex) (*ForgeIndex, error) {
		if cur == nil || !slices.Contains(cur.IDs.Value(), rec.ID.Value()) {
			return nil, store.ErrNoChange
		}
		cur.IDs = store.Set(slices.DeleteFunc(cur.IDs.Value(), func(id string) bool { return id == rec.ID.Value() }))
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
	secret, err := NewSecret(forge.Kind(rec.Kind.Value()))
	if err != nil {
		return nil, err
	}
	now := s.Registry.now().UTC()
	out, err := store.CAS(ctx, s.Registry.Control, ForgeKey(rec.ID.Value()), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil {
			return nil, api.NotFound("forge integration")
		}
		cur.PrevSecret, cur.PrevUntil, cur.Secret = store.Set(cur.Secret.Value()), store.Set(now.Add(prevSecretFor)), store.Set(secret)
		return cur, nil
	})
	if err != nil {
		return nil, storageErr(err)
	}
	_, a, err := s.Registry.Adapter(ctx, rec.ID.Value())
	if err != nil {
		return nil, storageErr(err)
	}
	var failed []string
	for rid := range out.Repos.Value() {
		var id int64
		if _, err := fmt.Sscan(rid, &id); err != nil {
			continue
		}
		link, err := s.Registry.Link(ctx, rec.ID.Value(), id)
		if err != nil {
			continue // intent-first entry without a link
		}
		repo := forge.Repo{Forge: forge.Kind(rec.Kind.Value()), ID: id, FullName: link.FullName.Value(), CloneURL: link.CloneURL.Value()}
		err = a.EnsureWebhook(ctx, repo, HookURL(s.BaseURL, rec.ID.Value()), forge.NewSecret(secret))
		s.Registry.Report(ctx, rec.ID.Value(), err)
		if err != nil && !errors.Is(err, forge.ErrUnsupported) {
			failed = append(failed, link.FullName.Value())
		}
	}
	if len(failed) > 0 {
		s.Registry.log().Warn("forge: webhook not re-ensured after rotation; the previous secret works for 24 h",
			"integration", rec.ID.Value(), "repositories", failed)
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
	_, a, err := s.Registry.Adapter(ctx, rec.ID.Value())
	if err != nil {
		return nil, storageErr(err)
	}
	out := &forgev1.TestForgeIntegrationResponse{}
	if name := req.Msg.GetRepoFullName(); name != "" {
		repo, err := a.LookupRepo(ctx, name)
		out.Health, out.Message = health(rec.ID.Value(), err)
		if err == nil {
			out.RepoId, out.Message = repo.ID, fmt.Sprintf("repository %s (id %d) is reachable", repo.FullName, repo.ID)
		}
		s.Registry.Report(ctx, rec.ID.Value(), err)
		return connect.NewResponse(out), nil
	}
	if forge.Kind(rec.Kind.Value()) != forge.Forgejo {
		out.Health, out.Message = forgev1.Health(rec.Health.Value()), "name a repository to check the credentials against the forge"
		return connect.NewResponse(out), nil
	}
	// Forgejo: listing the bot's tokens needs the bot password; nothing is older than year 1.
	_, err = a.SweepCheckouts(ctx, time.Time{})
	out.Health, out.Message = health(rec.ID.Value(), err)
	s.Registry.Report(ctx, rec.ID.Value(), err)
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
		for i := len(ring.Entries.Value()) - 1; i >= 0 && len(out.Deliveries) < ListDeliveries; i-- {
			e := ring.Entries.Value()[i]
			out.Deliveries = append(out.Deliveries, &forgev1.Delivery{DeliveryId: e.ID, Event: e.Event, Result: e.Result,
				BuildId: e.Build, ReceivedAt: timestamppb.New(e.Received)})
		}
	}
	return connect.NewResponse(out), nil
}
