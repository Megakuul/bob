package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/remote"
	"github.com/megakuul/bob/pkg/engine/store"
)

// credEntry is one server of credentials.json (SPEC §02.8 Files). Nix is bob's own addition:
// the cache configuration of the last successful WhoAmI, so that every command can write the
// same nix.conf (R-DEV-004) without asking the server.
type credEntry struct {
	Kind         string          `json:"kind"` // user | service
	AccessToken  string          `json:"accessToken"`
	RefreshToken string          `json:"refreshToken,omitempty"`
	Expires      string          `json:"expires,omitempty"` // RFC 3339; "" = no expiry known
	User         string          `json:"user,omitempty"`
	Nix          *remote.NixInfo `json:"nix,omitempty"`
}

// expiresAt is the parsed expiry; ok is false when none is known.
func (c *credEntry) expiresAt() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, c.Expires)
	return t, err == nil && t.Year() > 1
}

type credFile struct {
	V       int                   `json:"v"`
	Servers map[string]*credEntry `json:"servers"`
}

const (
	credName   = "credentials.json"
	configName = "config.json"
)

// cfgDir returns $XDG_CONFIG_HOME/bob, or a usage error when neither it nor HOME is known.
func (e *env) cfgDir() (string, error) {
	d := configDir(e.getenv)
	if d == "" {
		return "", diag.New(7002, "reason", "neither XDG_CONFIG_HOME nor HOME is set")
	}
	return d, nil
}

// withCreds runs fn on credentials.json under an exclusive lock and writes the result back
// (0600, temp file + rename) when fn reports a change. Concurrent bob processes (a refresh in
// one, a login in another) never lose each other's entries.
func (e *env) withCreds(fn func(c *credFile) (changed bool, err error)) error {
	dir, err := e.cfgDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	lf, err := os.OpenFile(filepath.Join(dir, ".credentials.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	defer lf.Close()
	for {
		err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("credentials lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	c, err := readCreds(dir)
	if err != nil {
		return err
	}
	changed, err := fn(c)
	if err != nil || !changed {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("credentials: %w", err)
	}
	return writeAtomic(filepath.Join(dir, credName), append(b, '\n'), 0o600)
}

// readCreds reads credentials.json; a missing file is an empty one.
func readCreds(dir string) (*credFile, error) {
	c := &credFile{V: 1, Servers: map[string]*credEntry{}}
	err := readJSON(filepath.Join(dir, credName), c)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, diag.New(4, "flag", filepath.Join(dir, credName), "what", "valid JSON ("+err.Error()+")")
	}
	if c.Servers == nil {
		c.Servers = map[string]*credEntry{}
	}
	c.V = 1
	return c, nil
}

// loadCreds reads credentials.json without locking (readers see a whole file: writes rename).
func (e *env) loadCreds() (*credFile, error) {
	dir, err := e.cfgDir()
	if err != nil {
		return nil, err
	}
	return readCreds(dir)
}

// writeAtomic replaces path through a temporary file and rename.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return diag.New(7002, "reason", err.Error())
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return diag.New(7002, "reason", err.Error())
	}
	if err := tmp.Close(); err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	return nil
}

// updateConfig applies fn to config.json (unknown keys are kept as they are) and writes it
// back (0644) when fn reports a change.
func (e *env) updateConfig(fn func(cfg map[string]json.RawMessage) bool) error {
	dir, err := e.cfgDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	p := filepath.Join(dir, configName)
	cfg := map[string]json.RawMessage{}
	if err := readJSON(p, &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return diag.New(4, "flag", p, "what", "valid JSON ("+err.Error()+")")
	}
	if !fn(cfg) {
		return nil
	}
	cfg["v"] = json.RawMessage("1")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return writeAtomic(p, append(b, '\n'), 0o644)
}

// userConfig is the part of config.json the server commands read.
type userConfig struct {
	DefaultServer string `json:"defaultServer"`
	Caches        []struct {
		URL       string `json:"url"`
		PublicKey string `json:"publicKey"`
	} `json:"caches"`
}

func (e *env) readConfig() userConfig {
	var c userConfig
	if dir := configDir(e.getenv); dir != "" {
		_ = readJSON(filepath.Join(dir, configName), &c)
	}
	return c
}

// normServer normalises a server URL: scheme (default https), host, path without a trailing
// slash. The result is the key of credentials.json.
func normServer(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return "", diag.New(4, "flag", raw, "what", "a server URL (https://bob.example.com)")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

// serverURL resolves the server of a command: the argument, --server, BOB_SERVER, the
// configured default server, else the only server with credentials. "" means none
// (BOB-E-4009 for commands that need one).
func (e *env) serverURL(arg string) (string, error) {
	for _, s := range []string{arg, e.a.str("server"), e.getenv("BOB_SERVER"), e.readConfig().DefaultServer} {
		if s != "" {
			return normServer(s)
		}
	}
	if c, err := e.loadCreds(); err == nil && len(c.Servers) == 1 {
		for u := range c.Servers {
			return u, nil
		}
	}
	return "", nil
}

// noServer is BOB-E-4009 "no bob server configured".
func noServer() *diag.Error { return diag.New(4009, "variant", "none") }

// substituters are bob's binary caches for this run (R-DEV-004): the caches of config.json and
// every logged-in server whose cache configuration is known, with its token in bob's netrc.
// skip names servers left out (unreachable, R-NIX-029).
func (e *env) substituters(c *credFile, skip map[string]bool) []store.Substituter {
	var out []store.Substituter
	for _, k := range e.readConfig().Caches {
		if k.URL != "" {
			out = append(out, store.Substituter{URL: strings.TrimRight(k.URL, "/"), PublicKey: k.PublicKey})
		}
	}
	if c == nil {
		return out
	}
	urls := make([]string, 0, len(c.Servers))
	for u := range c.Servers {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	for _, u := range urls {
		ent := c.Servers[u]
		if skip[u] || ent == nil || ent.Nix == nil || ent.AccessToken == "" {
			continue
		}
		sub := ent.Nix.Substituter
		if sub == "" {
			sub = u + "/nix"
		}
		if strings.ContainsAny(ent.AccessToken, " \t\r\n#") {
			continue // cannot be written to netrc
		}
		out = append(out, store.Substituter{URL: strings.TrimRight(sub, "/"), PublicKey: strings.Join(ent.Nix.PublicKeys, " "),
			NetrcMachine: netrcHost(sub), NetrcPassword: ent.AccessToken})
	}
	return out
}

// netrcHost is the netrc machine of a substituter URL.
func netrcHost(sub string) string {
	if pu, err := url.Parse(sub); err == nil && pu.Hostname() != "" {
		return pu.Hostname()
	}
	return remote.Host(sub)
}
