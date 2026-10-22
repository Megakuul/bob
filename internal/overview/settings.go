package overview

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	settingsv1 "github.com/megakuul/bob/internal/gen/bob/settings/v1"
	"github.com/megakuul/bob/internal/gen/bob/settings/v1/settingsv1connect"
	"github.com/megakuul/bob/internal/store"
)

// SettingsRecord is set/server: the server settings of SettingsService (SPEC §05.4.2
// `set/<name>`; one document holds every setting so that the API's etag covers them all).
// Zero fields mean the default.
type SettingsRecord struct {
	Name store.KeyField  `pk:"set" cbor:"-"` // "server"
	ETag store.ETagField `etag:"true" cbor:"-"`

	ServerName   store.DataField[string] `cbor:"sn,omitempty"`
	BaseURL      store.DataField[string] `cbor:"bu,omitempty"`
	LogsDays     store.DataField[int32]  `cbor:"rl,omitempty"`
	AnalyticsDay store.DataField[int32]  `cbor:"ra,omitempty"`
	BuildsDays   store.DataField[int32]  `cbor:"rb,omitempty"`
	CacheDays    store.DataField[int32]  `cbor:"rc,omitempty"`
	StagingDays  store.DataField[int32]  `cbor:"rs,omitempty"`
	CLIIdleDays  store.DataField[int32]  `cbor:"ci,omitempty"`
	CLIMaxDays   store.DataField[int32]  `cbor:"cm,omitempty"`
	BrowserIdleH store.DataField[int32]  `cbor:"bi,omitempty"`
	BrowserMaxD  store.DataField[int32]  `cbor:"bm,omitempty"`
	SATokenMaxD  store.DataField[int32]  `cbor:"sm,omitempty"`
	LiveLogOff   store.DataField[bool]   `cbor:"lo,omitempty"`
	MaxDownloads store.DataField[int32]  `cbor:"md,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *SettingsRecord) Draft() *SettingsRecord {
	return &SettingsRecord{Name: r.Name,
		ETag:         r.ETag,
		ServerName:   store.Carry(r.ServerName),
		BaseURL:      store.Carry(r.BaseURL),
		LogsDays:     store.Carry(r.LogsDays),
		AnalyticsDay: store.Carry(r.AnalyticsDay),
		BuildsDays:   store.Carry(r.BuildsDays),
		CacheDays:    store.Carry(r.CacheDays),
		StagingDays:  store.Carry(r.StagingDays),
		CLIIdleDays:  store.Carry(r.CLIIdleDays),
		CLIMaxDays:   store.Carry(r.CLIMaxDays),
		BrowserIdleH: store.Carry(r.BrowserIdleH),
		BrowserMaxD:  store.Carry(r.BrowserMaxD),
		SATokenMaxD:  store.Carry(r.SATokenMaxD),
		LiveLogOff:   store.Carry(r.LiveLogOff),
		MaxDownloads: store.Carry(r.MaxDownloads)}
}

// Defaults of SPEC §6.5.13.
const (
	defaultServerName   = "bob"
	defaultLogsDays     = 30
	defaultAnalytics    = 400
	defaultBuildsDays   = 365
	defaultCacheDays    = 90
	defaultStagingDays  = 7
	defaultCLIIdle      = 7
	defaultCLIMax       = 30
	defaultBrowserIdle  = 12
	defaultBrowserMax   = 7
	defaultSATokenMax   = 365
	defaultMaxDownloads = 256
)

// Settings is SettingsService: GetSettings and UpdateSettings here, the signing-key RPCs from
// Keys (promote.SettingsService). The stored values are what the API shows; the components
// that read them from the environment today switch over when they read set/server (NEEDS).
type Settings struct {
	settingsv1connect.SettingsServiceHandler // the key RPCs; nil = Unimplemented

	Control *store.Bucket
	// Defaults are the values shown for unset fields beyond the SPEC defaults (the server's
	// configured name and base URL).
	ServerName, BaseURL string

	cache store.Cache[SettingsRecord]
}

var _ settingsv1connect.SettingsServiceHandler = (*Settings)(nil)

func pick(v, def int32) int32 {
	if v > 0 {
		return v
	}
	return def
}

// read returns set/server (zero record when absent).
func (s *Settings) read(ctx context.Context) (*SettingsRecord, error) {
	rec, _, err := store.GetCached(ctx, s.Control, &s.cache, &SettingsRecord{Name: store.ID("server")})
	if errors.Is(err, store.ErrNotFound) {
		return &SettingsRecord{Name: store.ID("server")}, nil
	}
	return rec, err
}

// CacheDays is retention.cache_unreferenced_days (the default when unreadable).
func (s *Settings) CacheDays(ctx context.Context) int32 {
	rec, err := s.read(ctx)
	if err != nil {
		return defaultCacheDays
	}
	return pick(rec.CacheDays.Value(), defaultCacheDays)
}

func (s *Settings) message(r *SettingsRecord) *settingsv1.Settings {
	name := r.ServerName.Value()
	if name == "" {
		name = s.ServerName
	}
	if name == "" {
		name = defaultServerName
	}
	base := r.BaseURL.Value()
	if base == "" {
		base = s.BaseURL
	}
	return &settingsv1.Settings{ServerName: name, BaseUrl: base,
		Retention: &settingsv1.Retention{LogsDays: pick(r.LogsDays.Value(), defaultLogsDays), AnalyticsDays: pick(r.AnalyticsDay.Value(), defaultAnalytics),
			BuildsDays: pick(r.BuildsDays.Value(), defaultBuildsDays), CacheUnreferencedDays: pick(r.CacheDays.Value(), defaultCacheDays),
			StagingDays: pick(r.StagingDays.Value(), defaultStagingDays)},
		Sessions: &settingsv1.SessionPolicy{CliIdleDays: pick(r.CLIIdleDays.Value(), defaultCLIIdle), CliMaxDays: pick(r.CLIMaxDays.Value(), defaultCLIMax),
			BrowserIdleHours: pick(r.BrowserIdleH.Value(), defaultBrowserIdle), BrowserMaxDays: pick(r.BrowserMaxD.Value(), defaultBrowserMax),
			SaTokenMaxDays: pick(r.SATokenMaxD.Value(), defaultSATokenMax)},
		LiveLogEnabled: !r.LiveLogOff.Value(), MaxParallelDownloads: pick(r.MaxDownloads.Value(), defaultMaxDownloads), Etag: store.ETagOf(r.ETag)}
}

// GetSettings implements SettingsService.
func (s *Settings) GetSettings(ctx context.Context, _ *connect.Request[settingsv1.GetSettingsRequest]) (*connect.Response[settingsv1.GetSettingsResponse], error) {
	rec, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&settingsv1.GetSettingsResponse{Settings: s.message(rec)}), nil
}

var serverNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// rangeCheck is one bounded integer setting (0 = default).
type rangeCheck struct {
	field    string
	v        int32
	min, max int32
}

func checkRanges(cs ...rangeCheck) error {
	for _, c := range cs {
		if c.v != 0 && (c.v < c.min || c.v > c.max) {
			return api.InvalidArgument(c.field, fmt.Sprintf("must be between %d and %d (0 = default)", c.min, c.max))
		}
	}
	return nil
}

func checkBaseURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return api.InvalidArgument("settings.baseUrl", "base_url must be an http(s) URL without path, e.g. https://bob.example")
	}
	return nil
}

// UpdateSettings implements SettingsService for the fields named in update_mask (server_name,
// base_url, retention, sessions, live_log_enabled, max_parallel_downloads; retention and
// sessions are replaced as a whole). An etag, when given, must match (decision D-IB4-3).
func (s *Settings) UpdateSettings(ctx context.Context, req *connect.Request[settingsv1.UpdateSettingsRequest]) (*connect.Response[settingsv1.UpdateSettingsResponse], error) {
	in := req.Msg.GetSettings()
	if in == nil {
		return nil, api.InvalidArgument("settings", "settings is required")
	}
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "server_name", "base_url", "retention", "sessions",
		"live_log_enabled", "max_parallel_downloads"); err != nil {
		return nil, err
	}
	r, ss := in.GetRetention(), in.GetSessions()
	if in.GetServerName() != "" && !serverNameRE.MatchString(in.GetServerName()) {
		return nil, api.InvalidArgument("settings.serverName", "1-63 characters A-Z, a-z, 0-9, '.', '_' and '-'")
	}
	if err := checkBaseURL(in.GetBaseUrl()); err != nil {
		return nil, err
	}
	if err := checkRanges(
		rangeCheck{"settings.retention.logsDays", r.GetLogsDays(), 1, 3650},
		rangeCheck{"settings.retention.analyticsDays", r.GetAnalyticsDays(), 1, maxDays},
		rangeCheck{"settings.retention.buildsDays", r.GetBuildsDays(), 1, 3650},
		rangeCheck{"settings.retention.cacheUnreferencedDays", r.GetCacheUnreferencedDays(), 1, 3650},
		rangeCheck{"settings.retention.stagingDays", r.GetStagingDays(), 1, 365},
		rangeCheck{"settings.sessions.cliIdleDays", ss.GetCliIdleDays(), 1, 365},
		rangeCheck{"settings.sessions.cliMaxDays", ss.GetCliMaxDays(), 1, 3650},
		rangeCheck{"settings.sessions.browserIdleHours", ss.GetBrowserIdleHours(), 1, 720},
		rangeCheck{"settings.sessions.browserMaxDays", ss.GetBrowserMaxDays(), 1, 365},
		rangeCheck{"settings.sessions.saTokenMaxDays", ss.GetSaTokenMaxDays(), 1, 3650},
		rangeCheck{"settings.maxParallelDownloads", in.GetMaxParallelDownloads(), 1, 10000},
	); err != nil {
		return nil, err
	}
	etag := in.GetEtag()
	rec, err := store.CAS(ctx, s.Control, &SettingsRecord{Name: store.ID("server")}, func(cur *SettingsRecord) (*SettingsRecord, error) {
		next := SettingsRecord{Name: store.ID("server")}
		if cur != nil {
			next = *cur
		}
		if etag != "" && etag != store.ETagOf(next.ETag) {
			return nil, api.Aborted("the settings were changed by someone else (etag mismatch); reload and retry")
		}
		for _, p := range req.Msg.GetUpdateMask().GetPaths() {
			switch p {
			case "server_name", "serverName":
				next.ServerName = store.Set(in.GetServerName())
			case "base_url", "baseUrl":
				next.BaseURL = store.Set(strings.TrimRight(in.GetBaseUrl(), "/"))
			case "retention":
				next.LogsDays, next.AnalyticsDay, next.BuildsDays = store.Set(r.GetLogsDays()), store.Set(r.GetAnalyticsDays()), store.Set(r.GetBuildsDays())
				next.CacheDays, next.StagingDays = store.Set(r.GetCacheUnreferencedDays()), store.Set(r.GetStagingDays())
			case "sessions":
				next.CLIIdleDays, next.CLIMaxDays, next.BrowserIdleH = store.Set(ss.GetCliIdleDays()), store.Set(ss.GetCliMaxDays()), store.Set(ss.GetBrowserIdleHours())
				next.BrowserMaxD, next.SATokenMaxD = store.Set(ss.GetBrowserMaxDays()), store.Set(ss.GetSaTokenMaxDays())
			case "live_log_enabled", "liveLogEnabled":
				next.LiveLogOff = store.Set(!in.GetLiveLogEnabled())
			case "max_parallel_downloads", "maxParallelDownloads":
				next.MaxDownloads = store.Set(in.GetMaxParallelDownloads())
			}
		}
		if pick(next.CLIIdleDays.Value(), defaultCLIIdle) > pick(next.CLIMaxDays.Value(), defaultCLIMax) {
			return nil, api.InvalidArgument("settings.sessions.cliIdleDays", "cli_idle_days must not exceed cli_max_days")
		}
		if pick(next.BrowserIdleH.Value(), defaultBrowserIdle) > 24*pick(next.BrowserMaxD.Value(), defaultBrowserMax) {
			return nil, api.InvalidArgument("settings.sessions.browserIdleHours", "browser_idle_hours must not exceed browser_max_days")
		}
		if cur != nil && next == *cur {
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, api.Aborted("the settings changed concurrently; retry")
	}
	if err != nil {
		return nil, err
	}
	if rec == nil {
		rec = &SettingsRecord{Name: store.ID("server")}
	}
	return connect.NewResponse(&settingsv1.UpdateSettingsResponse{Settings: s.message(rec)}), nil
}
