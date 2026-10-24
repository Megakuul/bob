package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/remote"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// loginResult is the `result` data of bob login / bob whoami.
type loginResult struct {
	Server   string   `json:"server"`
	User     string   `json:"user"`
	Kind     string   `json:"kind,omitempty"`
	Scope    string   `json:"scope,omitempty"`
	Expires  string   `json:"expires,omitempty"`
	Projects []string `json:"projects,omitempty"`
}

// runLogin is `bob login [<url>] [--device] [--token-stdin]` (SPEC §02.8, R-CLI-003): a token
// by PKCE (when a browser can be opened) or the device flow, or a service-account token from
// stdin; then AuthService/WhoAmI, credentials.json (0600), bob's nix.conf and netrc
// (R-DEV-004) and the default server.
func runLogin(e *env) int {
	if len(e.a.pos) > 1 {
		return e.out.fail(diag.New(4, "flag", "bob login", "what", "at most one server URL"))
	}
	arg := ""
	if len(e.a.pos) == 1 {
		arg = e.a.pos[0]
	}
	server, err := e.serverURL(arg)
	if err != nil {
		return e.out.fail(err)
	}
	if server == "" {
		return e.out.fail(noServer())
	}
	ent, who, err := e.authenticate(server)
	if err != nil {
		return e.out.fail(err)
	}
	ent.User = who.Login
	if ent.User == "" {
		ent.User = who.Principal.ID
	}
	ent.Nix = cleanNix(server, who.Nix)
	err = e.withCreds(func(c *credFile) (bool, error) {
		c.Servers[server] = ent
		return true, nil
	})
	if err != nil {
		return e.out.fail(err)
	}
	if err := e.updateConfig(func(cfg map[string]json.RawMessage) bool { return addServer(cfg, server, ent.Nix) }); err != nil {
		return e.out.fail(err)
	}
	e.configureNix()
	res := loginResult{Server: server, User: ent.User, Kind: ent.Kind, Expires: ent.Expires}
	e.out.result(res)
	valid := ""
	if exp, ok := ent.expiresAt(); ok {
		valid = "token valid " + humanDuration(time.Until(exp))
		if ent.Kind == "user" {
			valid += ", refreshed automatically"
		}
		valid = " (" + valid + ")"
	}
	e.out.printf("logged in to %s as %s%s\n", remote.Host(server), ent.User, valid)
	return e.out.finish(0, translate.Counts{}, nil)
}

// authenticate gets the credentials entry of a login and checks it with WhoAmI.
func (e *env) authenticate(server string) (*credEntry, *remote.WhoAmI, error) {
	ctx := e.ctx
	if e.a.bool("token-stdin") {
		tok, err := readToken(os.Stdin)
		if err != nil {
			return nil, nil, err
		}
		who, err := e.api(server, tok).WhoAmI(ctx)
		if err != nil {
			return nil, nil, apiDiag(server, remote.WhoAmIMethod, "", err)
		}
		ent := &credEntry{Kind: "service", AccessToken: tok}
		if t, err := time.Parse(time.RFC3339, who.TokenExpiresAt); err == nil && t.Year() > 1 {
			ent.Expires = t.UTC().Format(time.RFC3339)
		}
		return ent, who, nil
	}
	var (
		t   *remote.Tokens
		err error
	)
	api := e.api(server, "")
	if browser := e.browser(); browser != nil && !e.a.bool("device") {
		e.status("Opening your browser to log in to %s…\n", remote.Host(server))
		t, err = api.PKCE(ctx, browser)
		var be *browserError
		if errors.As(err, &be) {
			e.status("the browser did not start (%v); using the device flow\n", be.err)
			t, err = e.deviceLogin(api)
		}
	} else {
		t, err = e.deviceLogin(api)
	}
	if err != nil {
		return nil, nil, apiDiag(server, "/oauth/token", "", err)
	}
	who, err := e.api(server, t.AccessToken).WhoAmI(ctx)
	if err != nil {
		return nil, nil, apiDiag(server, remote.WhoAmIMethod, "", err)
	}
	ent := &credEntry{Kind: "user", AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Expires: expiry(t.ExpiresIn)}
	if who.Login == "" {
		who.Login = t.User
	}
	return ent, who, nil
}

// deviceLogin runs the device flow; the instructions go to stdout (stderr with --json).
func (e *env) deviceLogin(api *remote.Server) (*remote.Tokens, error) {
	d, err := api.StartDevice(e.ctx)
	if err != nil {
		return nil, err
	}
	mins := (d.ExpiresIn + 59) / 60
	e.status("Open %s and enter code %s (expires in %d min)\n", d.VerificationURI, d.UserCode, mins)
	e.status("Waiting… ")
	return api.PollDevice(e.ctx, d)
}

// status prints login progress: stdout for people, stderr in --json mode (stdout holds events).
func (e *env) status(format string, a ...any) {
	if e.out.quiet && !e.out.json {
		return
	}
	w := e.out.w
	if e.out.json {
		w = e.out.errw
	}
	fmt.Fprintf(w, format, a...)
}

// readToken reads one token line from r.
func readToken(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 8<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", diag.New(4, "flag", "--token-stdin", "what", "a token on stdin")
	}
	tok := strings.TrimSpace(line)
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", diag.New(4, "flag", "--token-stdin", "what", "a token on stdin")
	}
	return tok, nil
}

// browserError is a browser that could not be started (the login falls back to the device flow).
type browserError struct{ err error }

func (b *browserError) Error() string { return "browser: " + b.err.Error() }

// browser returns how to open a URL in a browser, or nil when none can be opened (SPEC §02.8:
// $BROWSER, or xdg-open with DISPLAY/WAYLAND_DISPLAY set and not over SSH).
func (e *env) browser() func(string) error {
	var argv []string
	if b := e.getenv("BROWSER"); b != "" {
		first, _, _ := strings.Cut(b, ":")
		argv = strings.Fields(first)
	} else if (e.getenv("DISPLAY") != "" || e.getenv("WAYLAND_DISPLAY") != "") && e.getenv("SSH_CONNECTION") == "" && e.getenv("SSH_TTY") == "" {
		if p, err := exec.LookPath("xdg-open"); err == nil {
			argv = []string{p}
		}
	}
	if len(argv) == 0 {
		return nil
	}
	return func(u string) error {
		args := append([]string{}, argv[1:]...)
		placed := false
		for i, a := range args {
			if strings.Contains(a, "%s") {
				args[i], placed = strings.ReplaceAll(a, "%s", u), true
			}
		}
		if !placed {
			args = append(args, u)
		}
		cmd := exec.Command(argv[0], args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		if err := cmd.Start(); err != nil {
			return &browserError{err}
		}
		go func() {
			defer func() { _ = recover() }()
			_ = cmd.Wait() // reap; the browser's exit status does not matter
		}()
		return nil
	}
}

// addServer records a newly logged-in server in config.json: defaultServer when unset, and the
// first server as registry "default" (SPEC §02.8).
func addServer(cfg map[string]json.RawMessage, server string, nix *remote.NixInfo) bool {
	changed := false
	var def string
	_ = json.Unmarshal(cfg["defaultServer"], &def)
	if def == "" {
		cfg["defaultServer"], _ = json.Marshal(server)
		changed = true
	}
	var regs []map[string]any
	_ = json.Unmarshal(cfg["registries"], &regs)
	if len(regs) == 0 {
		keys := []string{}
		if nix != nil {
			keys = nix.PublicKeys
		}
		cfg["registries"], _ = json.Marshal([]map[string]any{{"name": "default", "url": server, "publicKeys": keys}})
		changed = true
	}
	return changed
}

// configureNix rewrites bob's nix.conf and netrc for the current logins (R-DEV-004). The
// credentials are stored already, so a failure (a host without user namespaces) is a warning:
// the next command that opens the store writes the same files.
func (e *env) configureNix() {
	c, err := e.loadCreds()
	if err == nil {
		ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
		defer cancel()
		_, err = store.Open(ctx, store.Config{Substituters: e.substituters(c, nil)})
	}
	if err != nil {
		for _, d := range diagErrors(err) {
			e.out.diagnose(warning(d), "")
		}
	}
}

// humanDuration is "30 days", "1 hour", "45 minutes".
func humanDuration(d time.Duration) string {
	unit := func(n int, what string) string {
		if n == 1 {
			return "1 " + what
		}
		return fmt.Sprintf("%d %ss", n, what)
	}
	switch {
	case d >= 48*time.Hour:
		return unit(int((d+12*time.Hour)/(24*time.Hour)), "day")
	case d >= 90*time.Minute:
		return unit(int((d+30*time.Minute)/time.Hour), "hour")
	case d >= time.Minute:
		return unit(int((d+30*time.Second)/time.Minute), "minute")
	}
	return "less than a minute"
}

// runLogout is `bob logout [<url>]`: revoke the token (best effort), delete the credentials
// entry, and drop the server from bob's nix.conf and netrc.
func runLogout(e *env) int {
	if len(e.a.pos) > 1 {
		return e.out.fail(diag.New(4, "flag", "bob logout", "what", "at most one server URL"))
	}
	arg := ""
	if len(e.a.pos) == 1 {
		arg = e.a.pos[0]
	}
	server, err := e.serverURL(arg)
	if err != nil {
		return e.out.fail(err)
	}
	if server == "" {
		return e.out.fail(noServer())
	}
	var old *credEntry
	err = e.withCreds(func(c *credFile) (bool, error) {
		old = c.Servers[server]
		if old == nil {
			return false, nil
		}
		delete(c.Servers, server)
		return true, nil
	})
	if err != nil {
		return e.out.fail(err)
	}
	if old == nil {
		e.out.result(map[string]string{"server": server, "state": "not logged in"})
		e.out.printf("not logged in to %s\n", remote.Host(server))
		return e.out.finish(0, translate.Counts{}, nil)
	}
	if old.Kind == "user" {
		// A refresh token ends the whole CLI session; a service-account token belongs to its
		// account and is not revoked by a logout.
		tok := old.RefreshToken
		if tok == "" {
			tok = old.AccessToken
		}
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		_ = e.api(server, old.AccessToken).Revoke(ctx, tok)
		cancel()
	}
	e.configureNix()
	e.out.result(map[string]string{"server": server, "state": "logged out"})
	e.out.printf("logged out of %s\n", remote.Host(server))
	return e.out.finish(0, translate.Counts{}, nil)
}

// runWhoAmI is `bob whoami [<url>]`: the login of a server, checked with AuthService/WhoAmI.
func runWhoAmI(e *env) int {
	if len(e.a.pos) > 1 {
		return e.out.fail(diag.New(4, "flag", "bob whoami", "what", "at most one server URL"))
	}
	arg := ""
	if len(e.a.pos) == 1 {
		arg = e.a.pos[0]
	}
	api, err := e.session(e.ctx, arg)
	if err != nil {
		return e.out.fail(err)
	}
	who, err := api.WhoAmI(e.ctx)
	if err != nil {
		return e.out.fail(apiDiag(api.URL, remote.WhoAmIMethod, "", err))
	}
	if err := e.storeNix(api.URL, who.Nix); err != nil {
		return e.out.fail(err)
	}
	user := who.Login
	if user == "" {
		user = who.Principal.ID
	}
	e.out.result(loginResult{Server: api.URL, User: user, Scope: who.TokenScope, Expires: who.TokenExpiresAt, Projects: who.Projects})
	e.out.printf("%s on %s (%s)\n", user, remote.Host(api.URL), who.Principal.ID)
	if who.TokenScope != "" {
		e.out.printf("  token scope: %s\n", who.TokenScope)
	}
	if len(who.Projects) > 0 {
		e.out.printf("  projects: %s\n", strings.Join(who.Projects, ", "))
	}
	return e.out.finish(0, translate.Counts{}, nil)
}
