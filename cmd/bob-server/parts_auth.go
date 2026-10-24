package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/auth"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
)

// authParts wires identity (M15.2, M15.3) into parts: the authenticator of bob's tokens and
// session cookies, the access services, the OAuth/OIDC endpoints and the bootstrap admin. It
// needs the RBAC authorizer (rbacParts runs first).
func authParts(env *server.Env, parts *server.OrchestratorParts) {
	authz, ok := parts.Authz.(*rbac.Authorizer)
	if !ok || env.Storage == nil || env.Storage.Control == nil {
		env.Log.Error("identity not available: RBAC or the control bucket is missing")
		return
	}
	id, err := auth.New(auth.Config{
		Control: env.Storage.Control, Now: env.Now, Log: env.Log, RBAC: authz,
		Info: api.Info{Name: env.Config.Name, BaseURL: env.Config.BaseURL, Version: server.Version()},
	})
	if err != nil {
		env.Log.Error("identity not available", "err", err.Error())
		return
	}
	parts.Authn = append(parts.Authn, id)
	id.Services(&parts.Services)
	parts.Bootstrap = id.Bootstrap
	if parts.Routes == nil {
		parts.Routes = map[string]http.Handler{}
	}
	h := id.Handler()
	for _, p := range auth.Patterns() {
		parts.Routes[p] = h
	}
	// Minimal approval pages; uiParts replaces them with the embedded UI when it is built.
	pages := id.FallbackPages()
	for _, p := range auth.FallbackPatterns() {
		parts.Routes[p] = pages
	}
}

// adminCreateUser is `bob-server admin create-user --login <name> [--admin]` (SPEC §6.3.2):
// the password is read from stdin (first line); the control credentials come from the
// orchestrator's environment. It is the only way to create the first admin.
func adminCreateUser(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("create-user", flag.ContinueOnError)
	fs.SetOutput(stderr)
	login := fs.String("login", "", "login name of the new local user")
	admin := fs.Bool("admin", false, "bind the user to the built-in role admin on every project")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *login == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, "usage: bob-server admin create-user --login <name> [--admin]  (password from stdin)\n")
		return 2
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "✗ read the password from stdin: %v\n", err)
		return 1
	}
	password := auth.Secret(strings.TrimRight(line, "\r\n"))
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	cfg, err := store.StorageConfigFromEnv(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		return store.ExitConfig
	}
	st, err := store.OpenStorage(ctx, cfg, server.ModeOrchestrator)
	if err != nil {
		return report(stderr, err)
	}
	authz, err := rbac.New(st.Control)
	if err != nil {
		return report(stderr, err)
	}
	id, err := auth.New(auth.Config{Control: st.Control, Now: time.Now, RBAC: authz})
	if err != nil {
		return report(stderr, err)
	}
	uid, err := id.CreateLocalUser(ctx, auth.NewLocalUser{Login: *login, Password: password, Admin: *admin})
	if err != nil {
		fmt.Fprintf(stderr, "✗ create user %s: %s\n", *login, apiMessage(err))
		return 1
	}
	suffix := ""
	if *admin {
		suffix = " bound to admin@*"
	}
	fmt.Fprintf(stdout, "created user %s (%s)%s\n", *login, uid, suffix)
	return 0
}

// apiMessage is the message of an API error without the connect code prefix.
func apiMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}
