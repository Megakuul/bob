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
	CacheNow     store.DataField[bool]   `cbor:"rz,omitempty"` // CacheDays is an explicit 0, not the default
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
	return &SettingsRecord{
		Name:         r.Name,
		ETag:         r.ETag,
		ServerName:   store.Carry(r.ServerName),
		BaseURL:      store.Carry(r.BaseURL),
		LogsDays:     store.Carry(r.LogsDays),
		AnalyticsDay: store.Carry(r.AnalyticsDay),
		BuildsDays:   store.Carry(r.BuildsDays),
		CacheDays:    store.Carry(r.CacheDays),
		CacheNow:     store.Carry(r.CacheNow),
		StagingDays:  store.Carry(r.StagingDays),
		CLIIdleDays:  store.Carry(r.CLIIdleDays),
		CLIMaxDays:   store.Carry(r.CLIMaxDays),
		BrowserIdleH: store.Carry(r.BrowserIdleH),
		BrowserMaxD:  store.Carry(r.BrowserMaxD),
		SATokenMaxD:  store.Carry(r.SATokenMaxD),
		LiveLogOff:   store.Carry(r.LiveLogOff),
		MaxDownloads: store.Carry(r.MaxDownloads),
	}
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

// zero reads a field of the settings record, which read returns without data fields when
// set/server does not exist (every setting then has its default).
func zero[T any](f store.DataField[T]) T {
	if f == nil {
		var z T
		return z
	}
	return f.Value()
}

// read returns set/server (a record without data fields when absent).
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
	return cacheDays(rec)
}

// cacheDays is the effective retention.cache_unreferenced_days of r. Unlike the other settings,
// an explicit 0 (set through the mask path retention.cache_unreferenced_days) is a valid value:
// unreferenced paths are collected on the next GC run (R-STORE-150). Absent means the default.
// Absent and 0 read the same from a stored record, so the explicit 0 is CacheNow.
func cacheDays(r *SettingsRecord) int32 {
	if zero(r.CacheNow) {
		return 0
	}
	return pick(zero(r.CacheDays), defaultCacheDays)
}

// settingsPaths are the update_mask paths UpdateSettings accepts (proto names; protojson
// lowerCamelCase paths are converted). retention and sessions replace the whole group, where 0
// means the default; a leaf path replaces one setting.
var settingsPaths = map[string]bool{
	"server_name": true, "base_url": true, "live_log_enabled": true, "max_parallel_downloads": true,
	"retention": true, "retention.logs_days": true, "retention.analytics_days": true, "retention.builds_days": true,
	"retention.cache_unreferenced_days": true, "retention.staging_days": true,
	"sessions": true, "sessions.cli_idle_days": true, "sessions.cli_max_days": true, "sessions.browser_idle_hours": true,
	"sessions.browser_max_days": true, "sessions.sa_token_max_days": true,
}

// maskPath turns a protojson mask path ("retention.cacheUnreferencedDays") into proto names
// ("retention.cache_unreferenced_days").
func maskPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// checkSettingsMask validates update_mask: required, and only the paths in settingsPaths.
func checkSettingsMask(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, api.InvalidArgument("updateMask", "update_mask is required")
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		n := maskPath(p)
		if !settingsPaths[n] {
			return nil, api.InvalidArgument("updateMask", fmt.Sprintf("field %q cannot be updated", p))
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Settings) message(r *SettingsRecord) *settingsv1.Settings {
	name := zero(r.ServerName)
	if name == "" {
		name = s.ServerName
	}
	if name == "" {
		name = defaultServerName
	}
	base := zero(r.BaseURL)
	if base == "" {
		base = s.BaseURL
	}
	return &settingsv1.Settings{ServerName: name, BaseUrl: base,
		Retention: &settingsv1.Retention{LogsDays: pick(zero(r.LogsDays), defaultLogsDays), AnalyticsDays: pick(zero(r.AnalyticsDay), defaultAnalytics),
			BuildsDays: pick(zero(r.BuildsDays), defaultBuildsDays), CacheUnreferencedDays: cacheDays(r),
			StagingDays: pick(zero(r.StagingDays), defaultStagingDays)},
		Sessions: &settingsv1.SessionPolicy{CliIdleDays: pick(zero(r.CLIIdleDays), defaultCLIIdle), CliMaxDays: pick(zero(r.CLIMaxDays), defaultCLIMax),
			BrowserIdleHours: pick(zero(r.BrowserIdleH), defaultBrowserIdle), BrowserMaxDays: pick(zero(r.BrowserMaxD), defaultBrowserMax),
			SaTokenMaxDays: pick(zero(r.SATokenMaxD), defaultSATokenMax)},
		LiveLogEnabled: !zero(r.LiveLogOff), MaxParallelDownloads: pick(zero(r.MaxDownloads), defaultMaxDownloads), Etag: store.ETagOf(r.ETag)}
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
// base_url, retention, sessions, live_log_enabled, max_parallel_downloads, and every leaf of
// retention and sessions; see settingsPaths). An etag, when given, must match (decision D-IB4-3).
func (s *Settings) UpdateSettings(ctx context.Context, req *connect.Request[settingsv1.UpdateSettingsRequest]) (*connect.Response[settingsv1.UpdateSettingsResponse], error) {
	in := req.Msg.GetSettings()
	if in == nil {
		return nil, api.InvalidArgument("settings", "settings is required")
	}
	paths, err := checkSettingsMask(req.Msg.GetUpdateMask().GetPaths())
	if err != nil {
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
		var cliIdle, cliMax, browserIdle, browserMax int32
		if cur != nil {
			next = *cur
			cliIdle, cliMax, browserIdle, browserMax = zero(cur.CLIIdleDays), zero(cur.CLIMaxDays), zero(cur.BrowserIdleH), zero(cur.BrowserMaxD)
		}
		if etag != "" && etag != store.ETagOf(next.ETag) { // next.ETag: cur's, or nil
			return nil, api.Aborted("the settings were changed by someone else (etag mismatch); reload and retry")
		}
		for _, p := range paths {
			switch p {
			case "server_name":
				next.ServerName = store.Set(in.GetServerName())
			case "base_url":
				next.BaseURL = store.Set(strings.TrimRight(in.GetBaseUrl(), "/"))
			case "retention":
				next.LogsDays, next.AnalyticsDay, next.BuildsDays = store.Set(r.GetLogsDays()), store.Set(r.GetAnalyticsDays()), store.Set(r.GetBuildsDays())
				next.CacheDays, next.CacheNow, next.StagingDays = store.Set(r.GetCacheUnreferencedDays()), store.Set(false), store.Set(r.GetStagingDays())
			case "retention.logs_days":
				next.LogsDays = store.Set(r.GetLogsDays())
			case "retention.analytics_days":
				next.AnalyticsDay = store.Set(r.GetAnalyticsDays())
			case "retention.builds_days":
				next.BuildsDays = store.Set(r.GetBuildsDays())
			case "retention.cache_unreferenced_days": // 0 = collect on the next GC run (cacheDays)
				next.CacheDays, next.CacheNow = store.Set(r.GetCacheUnreferencedDays()), store.Set(r.GetCacheUnreferencedDays() == 0)
			case "retention.staging_days":
				next.StagingDays = store.Set(r.GetStagingDays())
			case "sessions":
				cliIdle, cliMax, browserIdle, browserMax = ss.GetCliIdleDays(), ss.GetCliMaxDays(), ss.GetBrowserIdleHours(), ss.GetBrowserMaxDays()
				next.CLIIdleDays, next.CLIMaxDays, next.BrowserIdleH = store.Set(ss.GetCliIdleDays()), store.Set(ss.GetCliMaxDays()), store.Set(ss.GetBrowserIdleHours())
				next.BrowserMaxD, next.SATokenMaxD = store.Set(ss.GetBrowserMaxDays()), store.Set(ss.GetSaTokenMaxDays())
			case "sessions.cli_idle_days":
				cliIdle, next.CLIIdleDays = ss.GetCliIdleDays(), store.Set(ss.GetCliIdleDays())
			case "sessions.cli_max_days":
				cliMax, next.CLIMaxDays = ss.GetCliMaxDays(), store.Set(ss.GetCliMaxDays())
			case "sessions.browser_idle_hours":
				browserIdle, next.BrowserIdleH = ss.GetBrowserIdleHours(), store.Set(ss.GetBrowserIdleHours())
			case "sessions.browser_max_days":
				browserMax, next.BrowserMaxD = ss.GetBrowserMaxDays(), store.Set(ss.GetBrowserMaxDays())
			case "sessions.sa_token_max_days":
				next.SATokenMaxD = store.Set(ss.GetSaTokenMaxDays())
			case "live_log_enabled":
				next.LiveLogOff = store.Set(!in.GetLiveLogEnabled())
			case "max_parallel_downloads":
				next.MaxDownloads = store.Set(in.GetMaxParallelDownloads())
			}
		}
		if pick(cliIdle, defaultCLIIdle) > pick(cliMax, defaultCLIMax) {
			return nil, api.InvalidArgument("settings.sessions.cliIdleDays", "cli_idle_days must not exceed cli_max_days")
		}
		if pick(browserIdle, defaultBrowserIdle) > 24*pick(browserMax, defaultBrowserMax) {
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
