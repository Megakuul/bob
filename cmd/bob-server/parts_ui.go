package main

import (
	"errors"
	"net/http"

	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/ui"
)

// uiParts serves the embedded web UI (R-UI-001, SPEC §6.2 "GET /*") at "/". The UI renders the
// approval pages GET /oauth/consent and GET /device itself (D-UI-7), so they replace the
// identity's fallback pages; it runs after authParts. A binary built without the web build
// keeps the fallback pages and answers the UI routes with 503 and a hint.
func uiParts(env *server.Env, parts *server.OrchestratorParts) {
	if parts.Routes == nil {
		parts.Routes = map[string]http.Handler{}
	}
	u, err := ui.Embedded()
	if err != nil {
		if errors.Is(err, ui.ErrNotBuilt) {
			env.Log.Warn("web UI not available", "err", err.Error())
		} else {
			env.Log.Error("web UI not available", "err", err.Error())
		}
		parts.Routes["/"] = ui.NotBuilt()
		return
	}
	parts.Routes["/"] = u
	// /oauth/ is a pending prefix of internal/http, more specific than "/".
	parts.Routes["GET /oauth/consent"] = u
	parts.Routes["GET /device"] = u
}
