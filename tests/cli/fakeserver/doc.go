// Package fakeserver is a protocol-faithful stand-in for a bob server, for the client-side
// acceptance tests (bob login, bob install, bob registry): the OAuth endpoints of SPEC §06
// §6.3.2 (authorization code with PKCE, device flow, refresh, revoke), the connect JSON endpoints
// AuthService/{GetServerInfo, WhoAmI} and ReleaseService/ResolveRelease, and a signed binary
// cache under /nix serving real NARs. It logs every request (method, path, Authorization header,
// form) so tests can assert what bob sent. It exists because the real server is built later; the
// server's own behaviour is tested by the server suites.
package fakeserver
