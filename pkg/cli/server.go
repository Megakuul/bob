package cli

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/remote"
)

// refreshBefore: a token that expires within this window is refreshed before a server call
// (SPEC §02.8 Refresh).
const refreshBefore = 60 * time.Second

// api returns a client of the server with token.
func (e *env) api(server, token string) *remote.Server {
	return &remote.Server{URL: server, Token: token, UserAgent: "bob/" + bobVersion()}
}

// apiDiag maps a failed server call to the BOB-E code of SPEC §02.3: Unauthenticated → 4008,
// PermissionDenied and NotFound → 4010, no answer / Unavailable / ResourceExhausted → 4009,
// anything else → 4012 with the server's message. method names the permission.
func apiDiag(server, method, project string, err error) *diag.Error {
	host := remote.Host(server)
	var oe *remote.OAuthError
	if errors.As(err, &oe) {
		d := diag.New(4008, "server", server)
		d.Raw = []string{oe.Error()}
		return d
	}
	e, ok := remote.AsError(err)
	if !ok {
		if errors.Is(err, context.Canceled) {
			return diag.New(8001)
		}
		return diagErrors(err)[0]
	}
	if project == "" {
		project = "*"
	}
	var d *diag.Error
	switch {
	case e.Unreachable():
		reason := e.Message
		if e.Transport != nil {
			reason = transportReason(e.Transport)
		}
		if reason == "" {
			reason = "HTTP " + strconv.Itoa(e.Status)
		}
		d = diag.New(4009, "variant", "unreachable", "server", host, "reason", reason)
	case e.Code == "unauthenticated":
		d = diag.New(4008, "server", server)
	case e.Code == "permission_denied" || e.Code == "not_found":
		action := method
		if i := strings.LastIndexByte(method, '/'); i >= 0 {
			action = method[i+1:]
		}
		d = diag.New(4010, "server", host, "action", action, "project", project, "permission", method)
	default:
		reason := e.Message
		if reason == "" {
			reason = "HTTP " + strconv.Itoa(e.Status)
		}
		d = diag.New(4012, "id", "(not created)", "reason", reason)
	}
	d.Raw = []string{e.Error()}
	return d
}

// transportReason is a short reason for a connection failure (no Go error chains).
func transportReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "no such host"):
		return "unknown host"
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "timeout"):
		return "timed out"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// token returns a usable token for server: BOB_TOKEN when set, else the stored one, refreshed
// first when it expires within a minute (once; failure → BOB-E-4008). ok is false when there
// are no credentials for server.
func (e *env) token(ctx context.Context, server string) (tok string, ok bool, err error) {
	if t := strings.TrimSpace(e.getenv("BOB_TOKEN")); t != "" {
		return t, true, nil
	}
	c, err := e.loadCreds()
	if err != nil {
		return "", false, err
	}
	ent := c.Servers[server]
	if ent == nil || ent.AccessToken == "" {
		return "", false, nil
	}
	exp, known := ent.expiresAt()
	if ent.Kind == "service" || ent.RefreshToken == "" || !known || time.Until(exp) > refreshBefore {
		return ent.AccessToken, true, nil
	}
	t, err := e.api(server, "").Refresh(ctx, ent.RefreshToken)
	if err != nil {
		return "", true, apiDiag(server, "/oauth/token", "", err)
	}
	err = e.withCreds(func(c *credFile) (bool, error) {
		cur := c.Servers[server]
		if cur == nil {
			return false, nil // logged out meanwhile
		}
		cur.AccessToken = t.AccessToken
		if t.RefreshToken != "" {
			cur.RefreshToken = t.RefreshToken
		}
		cur.Expires = expiry(t.ExpiresIn)
		return true, nil
	})
	if err != nil {
		return "", true, err
	}
	return t.AccessToken, true, nil
}

// expiry is the RFC 3339 time expiresIn seconds from now ("" when unknown).
func expiry(expiresIn int64) string {
	if expiresIn <= 0 {
		return ""
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
}

// nixKey is a Nix public key "name:base64"; a hostile server must not be able to write other
// nix.conf settings through WhoAmI.nix.
var nixKey = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}:[A-Za-z0-9+/]{1,128}={0,2}$`)
})

// cleanNix keeps the parts of a server's cache configuration that are safe to write into
// nix.conf: an http(s) substituter URL without spaces and well-formed keys. nil when nothing
// usable is left.
func cleanNix(server string, n remote.NixInfo) *remote.NixInfo {
	out := &remote.NixInfo{}
	sub := n.Substituter
	if sub == "" {
		sub = server + "/nix"
	}
	if u, err := url.Parse(sub); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		!strings.ContainsAny(sub, " \t\r\n#\"'") {
		out.Substituter = strings.TrimRight(sub, "/")
	}
	for _, k := range n.PublicKeys {
		if nixKey().MatchString(k) {
			out.PublicKeys = append(out.PublicKeys, k)
		}
	}
	if out.Substituter == "" || len(out.PublicKeys) == 0 {
		return nil
	}
	return out
}

// preCheck is the credential pre-check of R-NIX-029 for every logged-in server, once per
// process, before Nix may substitute from it: refresh when due, then AuthService/WhoAmI.
// Unauthenticated → BOB-E-4008, PermissionDenied → 4010 (both stop the command); an
// unreachable server is warning 4009 (warns) and is left out of this run's substituters (skip).
// The cache configuration WhoAmI returns is stored for nix.conf.
func (e *env) preCheck(ctx context.Context) (skip map[string]bool, warns []*diag.Error, err error) {
	skip = map[string]bool{}
	c, err := e.loadCreds()
	if err != nil {
		return nil, nil, err
	}
	for server := range c.Servers {
		tok, ok, err := e.token(ctx, server)
		if err != nil {
			var d *diag.Error
			if errors.As(err, &d) && d.Code == 4009 {
				warns = append(warns, warning(d))
				skip[server] = true
				continue
			}
			return nil, nil, err
		}
		if !ok {
			continue
		}
		who, err := e.api(server, tok).WhoAmI(ctx)
		if err != nil {
			d := apiDiag(server, remote.WhoAmIMethod, "", err)
			if d.Code == 4009 {
				warns = append(warns, warning(d))
				skip[server] = true
				continue
			}
			return nil, nil, d
		}
		if err := e.storeNix(server, who.Nix); err != nil {
			return nil, nil, err
		}
	}
	return skip, warns, nil
}

// warning turns an error diagnostic into a warning.
func warning(d *diag.Error) *diag.Error {
	w := *d
	w.Severity = diag.SeverityWarning
	return &w
}

// storeNix records a server's cache configuration in its credentials entry (when it changed).
func (e *env) storeNix(server string, n remote.NixInfo) error {
	clean := cleanNix(server, n)
	return e.withCreds(func(c *credFile) (bool, error) {
		ent := c.Servers[server]
		if ent == nil || sameNix(ent.Nix, clean) {
			return false, nil
		}
		ent.Nix = clean
		return true, nil
	})
}

func sameNix(a, b *remote.NixInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Substituter == b.Substituter && strings.Join(a.PublicKeys, " ") == strings.Join(b.PublicKeys, " ")
}

// session is an authenticated client of the command's server (bob cancel, bob log --build,
// --remote): BOB-E-4009 without a server, 4008 without credentials.
func (e *env) session(ctx context.Context, arg string) (*remote.Server, error) {
	server, err := e.serverURL(arg)
	if err != nil {
		return nil, err
	}
	if server == "" {
		return nil, noServer()
	}
	tok, ok, err := e.token(ctx, server)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, diag.New(4008, "server", server)
	}
	return e.api(server, tok), nil
}
