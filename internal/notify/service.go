package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	notifyv1 "github.com/megakuul/bob/internal/gen/bob/notify/v1"
	"github.com/megakuul/bob/internal/gen/bob/notify/v1/notifyv1connect"
	"github.com/megakuul/bob/internal/store"
)

// Service implements NotifyService (SPEC §6.5.15).
type Service struct{ N *Notifier }

var _ notifyv1connect.NotifyServiceHandler = Service{}

var projectRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func storageErr(err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, store.ErrConflict):
		return api.Aborted("the webhook changed concurrently, try again")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return api.Unavailable("storage unavailable")
	}
	return err
}

func msg(h *HookRecord) *notifyv1.NotifyWebhook {
	return &notifyv1.NotifyWebhook{Id: h.ID.Value(), Url: h.URL.Value(), Events: h.Events.Value(), Projects: h.Projects.Value(), Enabled: h.Enabled.Value(),
		LastResult: h.LastResult.Value(), Etag: store.ETagOf(h.ETag)}
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return api.InvalidArgument("webhook.url", "the URL must be https, without credentials, at most 2048 characters")
	}
	return nil
}

func checkEvents(evs []string) error {
	if len(evs) == 0 {
		return api.InvalidArgument("webhook.events", "name at least one event: build.finished, release.published")
	}
	for _, e := range evs {
		if e != BuildFinished && e != ReleasePublished {
			return api.InvalidArgument("webhook.events", fmt.Sprintf("unknown event %q (build.finished, release.published)", e))
		}
	}
	return nil
}

func checkProjects(ps []string) error {
	if len(ps) > 100 {
		return api.InvalidArgument("webhook.projects", "at most 100 projects")
	}
	for _, p := range ps {
		if !projectRE.MatchString(p) {
			return api.InvalidArgument("webhook.projects", fmt.Sprintf("project id %q is invalid", p))
		}
	}
	return nil
}

func dedupe(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

// CreateNotifyWebhook creates a receiver and returns its signing secret once.
func (s Service) CreateNotifyWebhook(ctx context.Context, req *connect.Request[notifyv1.CreateNotifyWebhookRequest]) (*connect.Response[notifyv1.CreateNotifyWebhookResponse], error) {
	in := req.Msg.GetWebhook()
	if err := checkURL(in.GetUrl()); err != nil {
		return nil, err
	}
	if err := checkEvents(in.GetEvents()); err != nil {
		return nil, err
	}
	if err := checkProjects(in.GetProjects()); err != nil {
		return nil, err
	}
	b := make([]byte, 40)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	h := HookKey("wh-" + hex.EncodeToString(b[:8]))
	h.URL, h.Secret, h.Events, h.Projects, h.Enabled, h.Created = store.Set(in.GetUrl()), store.Set(hex.EncodeToString(b[8:])), store.Set(dedupe(in.GetEvents())),
		store.Set(dedupe(in.GetProjects())), store.Set(in.GetEnabled()), store.Set(s.N.Now().UTC())
	if _, err := store.CAS(ctx, s.N.Control, HookIndexKey(), func(cur *HookIndex) (*HookIndex, error) {
		if cur == nil {
			cur = HookIndexKey()
		}
		if len(cur.IDs.Value()) >= MaxHooks {
			return nil, api.FailedPrecondition(fmt.Sprintf("at most %d notification webhooks", MaxHooks))
		}
		cur.IDs = store.Set(append(cur.IDs.Value(), h.ID.Value()))
		return cur, nil
	}); err != nil {
		return nil, storageErr(err)
	}
	if err := store.Create(ctx, s.N.Control, h); err != nil {
		return nil, storageErr(err)
	}
	return connect.NewResponse(&notifyv1.CreateNotifyWebhookResponse{Webhook: msg(h), Secret: h.Secret.Value()}), nil
}

func (s Service) get(ctx context.Context, id string) (*HookRecord, error) {
	if _, err := store.Segment(id); err != nil {
		return nil, api.NotFound("notification webhook")
	}
	h, err := store.Get(ctx, s.N.Control, HookKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("notification webhook")
	}
	return h, storageErr(err)
}

// GetNotifyWebhook returns one receiver (without its secret).
func (s Service) GetNotifyWebhook(ctx context.Context, req *connect.Request[notifyv1.GetNotifyWebhookRequest]) (*connect.Response[notifyv1.GetNotifyWebhookResponse], error) {
	h, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&notifyv1.GetNotifyWebhookResponse{Webhook: msg(h)}), nil
}

// ListNotifyWebhooks lists the receivers (≤ 50).
func (s Service) ListNotifyWebhooks(ctx context.Context, _ *connect.Request[notifyv1.ListNotifyWebhooksRequest]) (*connect.Response[notifyv1.ListNotifyWebhooksResponse], error) {
	idx, err := store.Get(ctx, s.N.Control, HookIndexKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, storageErr(err)
	}
	out := &notifyv1.ListNotifyWebhooksResponse{}
	if idx == nil {
		return connect.NewResponse(out), nil
	}
	for _, id := range idx.IDs.Value() {
		h, err := store.Get(ctx, s.N.Control, HookKey(id))
		if errors.Is(err, store.ErrNotFound) {
			continue // intent-first entry of an unfinished create
		}
		if err != nil {
			return nil, storageErr(err)
		}
		out.Webhooks = append(out.Webhooks, msg(h))
	}
	return connect.NewResponse(out), nil
}

// UpdateNotifyWebhook changes url, events, projects and enabled.
func (s Service) UpdateNotifyWebhook(ctx context.Context, req *connect.Request[notifyv1.UpdateNotifyWebhookRequest]) (*connect.Response[notifyv1.UpdateNotifyWebhookResponse], error) {
	in := req.Msg.GetWebhook()
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "url", "events", "projects", "enabled"); err != nil {
		return nil, err
	}
	if _, err := s.get(ctx, in.GetId()); err != nil {
		return nil, err
	}
	out, err := store.CAS(ctx, s.N.Control, HookKey(in.GetId()), func(cur *HookRecord) (*HookRecord, error) {
		if cur == nil {
			return nil, api.NotFound("notification webhook")
		}
		if in.GetEtag() != "" && in.GetEtag() != store.ETagOf(cur.ETag) {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		for _, p := range req.Msg.GetUpdateMask().GetPaths() {
			switch p {
			case "url":
				if err := checkURL(in.GetUrl()); err != nil {
					return nil, err
				}
				cur.URL = store.Set(in.GetUrl())
			case "events":
				if err := checkEvents(in.GetEvents()); err != nil {
					return nil, err
				}
				cur.Events = store.Set(dedupe(in.GetEvents()))
			case "projects":
				if err := checkProjects(in.GetProjects()); err != nil {
					return nil, err
				}
				cur.Projects = store.Set(dedupe(in.GetProjects()))
			case "enabled":
				cur.Enabled = store.Set(in.GetEnabled())
			}
		}
		return cur, nil
	})
	if err != nil {
		return nil, storageErr(err)
	}
	return connect.NewResponse(&notifyv1.UpdateNotifyWebhookResponse{Webhook: msg(out)}), nil
}

// DeleteNotifyWebhook removes a receiver; queued deliveries to it are dropped.
func (s Service) DeleteNotifyWebhook(ctx context.Context, req *connect.Request[notifyv1.DeleteNotifyWebhookRequest]) (*connect.Response[notifyv1.DeleteNotifyWebhookResponse], error) {
	h, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := store.Delete(ctx, s.N.Control, h); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, storageErr(err)
	}
	if _, err := store.CAS(ctx, s.N.Control, HookIndexKey(), func(cur *HookIndex) (*HookIndex, error) {
		if cur == nil || !slices.Contains(cur.IDs.Value(), h.ID.Value()) {
			return nil, store.ErrNoChange
		}
		cur.IDs = store.Set(slices.DeleteFunc(cur.IDs.Value(), func(id string) bool { return id == h.ID.Value() }))
		return cur, nil
	}); err != nil {
		return nil, storageErr(err)
	}
	return connect.NewResponse(&notifyv1.DeleteNotifyWebhookResponse{}), nil
}
