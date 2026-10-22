package overview_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/megakuul/bob/internal/api"
	buildctl "github.com/megakuul/bob/internal/build"
	analyticsv1 "github.com/megakuul/bob/internal/gen/bob/analytics/v1"
	"github.com/megakuul/bob/internal/gen/bob/analytics/v1/analyticsv1connect"
	settingsv1 "github.com/megakuul/bob/internal/gen/bob/settings/v1"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/overview"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

type noFleet struct{}

func (noFleet) AddClass(context.Context, store.Class) (string, error) { return "", nil }
func (noFleet) Enqueue(context.Context, string, orchestrator.Job) (uint64, []string, error) {
	return 0, nil, nil
}

func TestSettingsAndAnalytics(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control, fleet := be.Open(t, "control"), be.Open(t, "fleet")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// settings: defaults, a masked update, etag, validation
	s := &overview.Settings{Control: control, BaseURL: "https://bob.example"}
	got, err := s.GetSettings(ctx, connect.NewRequest(&settingsv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Msg.GetSettings(); m.GetServerName() != "bob" || m.GetBaseUrl() != "https://bob.example" || m.GetRetention().GetLogsDays() != 30 ||
		m.GetRetention().GetCacheUnreferencedDays() != 90 || !m.GetLiveLogEnabled() || m.GetMaxParallelDownloads() != 256 {
		t.Errorf("default settings = %v", m)
	}
	upd := func(m *settingsv1.Settings, paths ...string) (*settingsv1.Settings, error) {
		r, err := s.UpdateSettings(ctx, connect.NewRequest(&settingsv1.UpdateSettingsRequest{Settings: m, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths}}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetSettings(), nil
	}
	m, err := upd(&settingsv1.Settings{ServerName: "acme", Retention: &settingsv1.Retention{CacheUnreferencedDays: 30}, LiveLogEnabled: false},
		"serverName", "retention", "liveLogEnabled")
	if err != nil || m.GetServerName() != "acme" || m.GetRetention().GetCacheUnreferencedDays() != 30 || m.GetRetention().GetLogsDays() != 30 ||
		m.GetLiveLogEnabled() || m.GetEtag() == "" {
		t.Fatalf("UpdateSettings = %v %v", m, err)
	}
	if d := s.CacheDays(ctx); d != 30 {
		t.Errorf("CacheDays = %d", d)
	}
	for _, c := range []struct {
		name  string
		m     *settingsv1.Settings
		paths []string
		want  connect.Code
	}{
		{"stale etag", &settingsv1.Settings{ServerName: "x", Etag: "stale"}, []string{"serverName"}, connect.CodeAborted},
		{"analytics over 400 days", &settingsv1.Settings{Retention: &settingsv1.Retention{AnalyticsDays: 401}}, []string{"retention"}, connect.CodeInvalidArgument},
		{"base URL with a path", &settingsv1.Settings{BaseUrl: "https://bob.example/x"}, []string{"baseUrl"}, connect.CodeInvalidArgument},
		{"idle beyond max", &settingsv1.Settings{Sessions: &settingsv1.SessionPolicy{CliIdleDays: 40, CliMaxDays: 30}}, []string{"sessions"}, connect.CodeInvalidArgument},
		{"unknown mask path", &settingsv1.Settings{}, []string{"nope"}, connect.CodeInvalidArgument},
		{"empty mask", &settingsv1.Settings{}, nil, connect.CodeInvalidArgument},
	} {
		if _, err := upd(c.m, c.paths...); code(err) != c.want {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}

	// analytics over build records and the §05 indexes
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	b, err := buildctl.New(buildctl.Config{Control: control, Jobs: fleet, Fleet: noFleet{}, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	pages := store.BuildPages(control, "p1")
	mk := func(n uint64, id, state string, created time.Time, dur time.Duration) {
		rec := &store.BuildRecord{ID: store.ID(id), Project: store.Set[string]("p1"), Number: store.Set(n), State: store.Set(state), Created: store.Set(created), Started: store.Set(created),
			Pipelines: store.Set([]store.PipelineRun{{Name: "ci", State: state}})}
		if buildctl.Terminal(state) {
			rec.Finished = store.Set(created.Add(dur))
		}
		if err := store.Create(ctx, control, rec); err != nil {
			t.Fatal(err)
		}
		if err := pages.Set(ctx, n-1, store.BuildEntry{N: n, Build: id, State: state, Created: created, Duration: dur}); err != nil {
			t.Fatal(err)
		}
	}
	day1, day2 := clock.Add(-26*time.Hour), clock.Add(-time.Hour)
	mk(1, "01aaaaaaaaaaaaaaaaaaaaaaa1", buildctl.StateSucceeded, day1, 10*time.Second)
	mk(2, "01aaaaaaaaaaaaaaaaaaaaaaa2", buildctl.StateFailed, day1, 20*time.Second)
	mk(3, "01aaaaaaaaaaaaaaaaaaaaaaa3", buildctl.StateSucceeded, day2, 30*time.Second)
	mk(4, "01aaaaaaaaaaaaaaaaaaaaaaa4", buildctl.StateRunning, day2, 0)
	if err := store.Change(ctx, control, &store.ProjectCounters{Project: store.ID("p1")}, func(*store.ProjectCounters) (*store.ProjectCounters, error) {
		return &store.ProjectCounters{Project: store.ID("p1"), Builds: store.Set[uint64](4)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Change(ctx, control, &store.RunningBuilds{Name: store.ID("running")}, func(*store.RunningBuilds) (*store.RunningBuilds, error) {
		return &store.RunningBuilds{Name: store.ID("running"), Builds: store.Set(map[string]store.RunningBuild{
			"01aaaaaaaaaaaaaaaaaaaaaaa4": {Project: "p1", N: 4}, "01aaaaaaaaaaaaaaaaaaaaaaa9": {Project: "p2", N: 1}})}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Change(ctx, control, &store.FailedBuilds{Name: store.ID("failed")}, func(*store.FailedBuilds) (*store.FailedBuilds, error) {
		return &store.FailedBuilds{Name: store.ID("failed"), Builds: store.Set([]store.FailedBuild{{Build: "01aaaaaaaaaaaaaaaaaaaaaaa2", Project: "p1", N: 2}})}, nil
	}); err != nil {
		t.Fatal(err)
	}
	a := &overview.Analytics{Builds: b, Control: control, Fleet: fleet, Now: func() time.Time { return clock }}

	srv, err := api.New(api.Config{Services: api.Services{Analytics: a},
		Authn: &api.StaticToken{Token: "adm", Principal: api.Principal{ID: "user:admin", Scope: "api", Bindings: []api.Binding{{Role: "admin", Project: "*"}}}},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cl := analyticsv1connect.NewAnalyticsServiceClient(ts.Client(), ts.URL+"/api")
	ovReq := connect.NewRequest(&analyticsv1.GetOverviewRequest{})
	ovReq.Header().Set("Authorization", "Bearer adm")
	ov, err := cl.GetOverview(ctx, ovReq)
	if err != nil {
		t.Fatal(err)
	}
	if r, f := ov.Msg.GetRunning(), ov.Msg.GetRecentFailures(); len(r) != 1 || r[0].GetNumber() != 4 || len(f) != 1 || f[0].GetNumber() != 2 {
		t.Errorf("GetOverview = %v", ov.Msg)
	}
	rfReq := connect.NewRequest(&analyticsv1.RecentFailuresRequest{ProjectId: "p2"})
	rfReq.Header().Set("Authorization", "Bearer adm")
	if rf, err := cl.RecentFailures(ctx, rfReq); err != nil || len(rf.Msg.GetBuilds()) != 0 {
		t.Errorf("RecentFailures{p2} = %v %v", rf, err)
	}
	h, err := a.BuildHistory(ctx, connect.NewRequest(&analyticsv1.BuildHistoryRequest{ProjectId: "p1", Days: 7}))
	if err != nil {
		t.Fatal(err)
	}
	if d := h.Msg.GetDays(); len(d) != 2 || d[0].GetSucceeded() != 1 || d[0].GetFailed() != 1 || d[0].GetP50DurationS() != 10 ||
		d[0].GetP90DurationS() != 20 || d[1].GetSucceeded() != 1 || d[1].GetP50DurationS() != 30 {
		t.Errorf("BuildHistory = %v", d)
	}
	if h, err := a.BuildHistory(ctx, connect.NewRequest(&analyticsv1.BuildHistoryRequest{ProjectId: "p1", Days: 1})); err != nil || len(h.Msg.GetDays()) != 1 {
		t.Errorf("BuildHistory{days: 1} = %v %v", h, err)
	}
	if h, err := a.BuildHistory(ctx, connect.NewRequest(&analyticsv1.BuildHistoryRequest{ProjectId: "p1", Days: 7, Pipeline: "other"})); err != nil || len(h.Msg.GetDays()) != 0 {
		t.Errorf("BuildHistory{pipeline: other} = %v %v", h, err)
	}
	for name, err := range map[string]error{
		"PackageDurations days 401": func() error {
			_, err := a.PackageDurations(ctx, connect.NewRequest(&analyticsv1.PackageDurationsRequest{ProjectId: "p1", Days: 401}))
			return err
		}(),
		"BuildHistory days -1": func() error {
			_, err := a.BuildHistory(ctx, connect.NewRequest(&analyticsv1.BuildHistoryRequest{ProjectId: "p1", Days: -1}))
			return err
		}(),
		"RecentFailures limit 101": func() error {
			_, err := a.RecentFailures(ctx, connect.NewRequest(&analyticsv1.RecentFailuresRequest{Limit: 101}))
			return err
		}(),
	} {
		if code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
}
