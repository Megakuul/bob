package overview

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/cache"
	cachev1 "github.com/megakuul/bob/internal/gen/bob/cache/v1"
	"github.com/megakuul/bob/internal/gen/bob/cache/v1/cachev1connect"
	"github.com/megakuul/bob/internal/project"
)

// Cache is CacheService. The statistics come from the daily cache GC sweep and the hit
// counters, neither of which records anything yet: GetCacheStats reports the retention
// setting and zero counts, ListTopPackages an empty list.
type Cache struct {
	Objects   *cache.Objects  // artifacts bucket (narinfos); nil: GetPath finds nothing
	Vis       *cache.VisStore // NixVisPrefix documents
	Scope     project.Scoper
	Retention func(ctx context.Context) int32 // retention.cache_unreferenced_days
}

var _ cachev1connect.CacheServiceHandler = (*Cache)(nil)

// GetCacheStats implements CacheService.
func (c *Cache) GetCacheStats(ctx context.Context, _ *connect.Request[cachev1.GetCacheStatsRequest]) (*connect.Response[cachev1.GetCacheStatsResponse], error) {
	out := &cachev1.GetCacheStatsResponse{RetentionUnreferencedDays: defaultCacheDays}
	if c.Retention != nil {
		out.RetentionUnreferencedDays = c.Retention(ctx)
	}
	return connect.NewResponse(out), nil
}

// GetPath implements CacheService: the narinfo of a served path, when the visibility document
// makes it visible to the caller (public, or GetPath on one of its projects); NotFound
// otherwise, the same answer as for an unknown path.
func (c *Cache) GetPath(ctx context.Context, req *connect.Request[cachev1.GetPathRequest]) (*connect.Response[cachev1.GetPathResponse], error) {
	hash := req.Msg.GetStoreHash()
	if !cache.ValidHash(hash) {
		return nil, api.InvalidArgument("storeHash", "store_hash must be the 32-character Nix base32 hash of a store path")
	}
	if c.Vis == nil || c.Objects == nil {
		return nil, api.NotFound("path")
	}
	doc, err := c.Vis.Get(ctx, hash)
	if err != nil {
		return nil, api.Unavailable("the cache is not available, try again")
	}
	if doc == nil {
		return nil, api.NotFound("path")
	}
	allowed, err := project.Allowed(ctx, c.Scope, cache.GetPathMethod)
	if err != nil {
		return nil, err
	}
	var projects []string
	for _, p := range doc.Projects {
		if allowed(p) {
			projects = append(projects, p)
		}
	}
	public := len(doc.Public) > 0
	if !public && len(projects) == 0 {
		return nil, api.NotFound("path")
	}
	body, _, err := c.Objects.Read(ctx, cache.NarInfoKey(hash), cache.MaxNarInfo)
	if errors.Is(err, cache.ErrNotFound) {
		return nil, api.NotFound("path")
	}
	if err != nil {
		return nil, api.Unavailable("the cache is not available, try again")
	}
	ni, err := cache.ParseNarInfo(body)
	if err != nil {
		return nil, fmt.Errorf("narinfo %s: %w", hash, err)
	}
	slices.Sort(projects)
	return connect.NewResponse(&cachev1.GetPathResponse{StorePath: ni.StorePath, NarSize: int64(ni.NarSize),
		FileSize: int64(ni.FileSize), References: ni.References, Signatures: ni.Sigs, Projects: projects, Public: public}), nil
}

// ListTopPackages implements CacheService (no hit counters are recorded yet: empty).
func (c *Cache) ListTopPackages(_ context.Context, req *connect.Request[cachev1.ListTopPackagesRequest]) (*connect.Response[cachev1.ListTopPackagesResponse], error) {
	if l := req.Msg.GetLimit(); l < 0 || l > maxLimit {
		return nil, api.InvalidArgument("limit", fmt.Sprintf("limit must be between 1 and %d", maxLimit))
	}
	if p := req.Msg.GetProjectId(); p != "" && !project.ValidID(p) {
		return nil, api.InvalidArgument("projectId", "invalid project id")
	}
	return connect.NewResponse(&cachev1.ListTopPackagesResponse{}), nil
}
