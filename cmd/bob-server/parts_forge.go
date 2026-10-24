package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/megakuul/bob/internal/fleetsrv"
	"github.com/megakuul/bob/internal/forge/integration"
	"github.com/megakuul/bob/internal/netguard"
	"github.com/megakuul/bob/internal/notify"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
)

// forgeWiring is what the forge and notification parts hand to later features: the build
// controller sets Intake.Builds and publishes build.finished through Notifier (M17.2).
type forgeWiring struct {
	Registry *integration.Registry
	Intake   *integration.Intake
	Notifier *notify.Notifier
}

// forgeParts wires forge integrations (M17.1) and outgoing webhooks (M17.4): ForgeService, the
// webhook intake POST /hooks/{integration}, the forge side of GetCheckoutCredential (the
// adapters replace the fleet's placeholder minter; the forge-sweeper also sweeps by token name
// every 5 min, SPEC §6.9.6), NotifyService and the notify controller. It runs after fleetParts.
func forgeParts(env *server.Env, parts *server.OrchestratorParts) *forgeWiring {
	if env.Storage == nil || env.Storage.Control == nil || env.Storage.Fleet == nil {
		env.Log.Error("forge integrations not available: the control or fleet bucket is missing")
		return nil
	}
	// Forge API calls and webhook deliveries go to admin-configured URLs: only public addresses
	// unless server.outbound_allow names more (I-R2 F8; ConfigFromEnv validated the list).
	outbound, err := netguard.New(env.Config.OutboundAllow)
	if err != nil {
		env.Log.Error("forge integrations not available: server.outbound_allow", "err", err)
		return nil
	}
	reg := &integration.Registry{Control: env.Storage.Control, Now: env.Now, Log: env.Log, Outbound: outbound}
	w := &forgeWiring{Registry: reg, Intake: &integration.Intake{Registry: reg},
		Notifier: notify.New(env.Storage.Control, env.Now, env.Log)}
	w.Notifier.Outbound(outbound)
	// Webhook URLs need an absolute base: server.base_url, else the listen address (as workers).
	parts.Services.Forge = &integration.Service{Registry: reg, BaseURL: endpoint(env.Config)}
	parts.Services.Notify = notify.Service{N: w.Notifier}
	if parts.Routes == nil {
		parts.Routes = map[string]http.Handler{}
	}
	parts.Routes[integration.HookPattern] = w.Intake

	minter := &integration.Minter{Registry: reg}
	if wc, ok := parts.Services.WorkerControl.(*fleetsrv.WorkerControl); ok && wc.F != nil && wc.F.Checkouts != nil {
		wc.F.Checkouts.Forge = minter
	} else {
		env.Log.Error("forge checkout credentials not wired: the fleet parts are missing")
	}
	for _, c := range parts.Controllers {
		if ctl, ok := c.(*orchestrator.Controller); ok && ctl.ID == orchestrator.CtlForgeSweeper {
			pass := ctl.Pass
			ctl.Pass = func(ctx context.Context, r *orchestrator.Run) error {
				return errors.Join(pass(ctx, r), minter.SweepNames(ctx))
			}
		}
	}
	parts.Controllers = append(parts.Controllers, w.Notifier.Controller(env.Storage.Fleet, env.Config.Name, store.LeaseSkew))
	return w
}
