package remote

import "context"

// Release methods of bob.release.v1.ReleaseService and bob.auth.v1.AuthService that bob install
// and bob registry use (SPEC §02.8, §06 §6.8).
const (
	ResolveReleaseMethod = "bob.release.v1.ReleaseService/ResolveRelease"
	ListReleasesMethod   = "bob.release.v1.ReleaseService/ListReleases"
	ServerInfoMethod     = "bob.auth.v1.AuthService/GetServerInfo"
)

// PlatformClosure is the installable closure of a release for one platform.
type PlatformClosure struct {
	Platform        string   `json:"platform"`
	Roots           []string `json:"roots"`
	ClosureNarBytes Int64    `json:"closureNarBytes"`
}

// Asset is a downloadable file of a release.
type Asset struct {
	File     string `json:"file"`
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

// ImageRef is an OCI image of a release.
type ImageRef struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// Release is the part of bob.release.v1.Release the CLI reads.
type Release struct {
	ID         string            `json:"id"`
	ProjectID  string            `json:"projectId"`
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Visibility string            `json:"visibility"`
	Closures   []PlatformClosure `json:"closures"`
	Assets     []Asset           `json:"assets"`
	Images     []ImageRef        `json:"images"`
}

// Resolved is bob.release.v1.ResolveReleaseResponse.
type Resolved struct {
	Release      Release          `json:"release"`
	Closure      *PlatformClosure `json:"closure"`
	Substituters []string         `json:"substituters"`
	PublicKeys   []string         `json:"publicKeys"`
}

// ResolveRelease asks the registry for the highest release of project/name matching
// constraint ("" = highest non-pre-release) and its closure for platform (Connect GET: the
// method has no side effects).
func (s *Server) ResolveRelease(ctx context.Context, project, name, constraint, platform string) (*Resolved, error) {
	req := struct {
		ProjectID  string `json:"projectId"`
		Name       string `json:"name"`
		Constraint string `json:"constraint,omitempty"`
		Platform   string `json:"platform"`
	}{project, name, constraint, platform}
	var r Resolved
	if err := s.Get(ctx, ResolveReleaseMethod, req, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListReleases returns up to 100 releases of project (newest version first).
func (s *Server) ListReleases(ctx context.Context, project string) ([]Release, error) {
	req := struct {
		PageSize  int32  `json:"pageSize"`
		ProjectID string `json:"projectId"`
	}{100, project}
	var r struct {
		Releases []Release `json:"releases"`
	}
	if err := s.Get(ctx, ListReleasesMethod, req, &r); err != nil {
		return nil, err
	}
	return r.Releases, nil
}

// ServerInfo is the part of bob.auth.v1.GetServerInfoResponse the CLI reads.
type ServerInfo struct {
	ServerName string  `json:"serverName"`
	BaseURL    string  `json:"baseUrl"`
	Nix        NixInfo `json:"nix"`
}

// GetServerInfo calls AuthService/GetServerInfo (anonymous; the caller leaves Token empty).
func (s *Server) GetServerInfo(ctx context.Context) (*ServerInfo, error) {
	var r ServerInfo
	if err := s.Get(ctx, ServerInfoMethod, struct{}{}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
