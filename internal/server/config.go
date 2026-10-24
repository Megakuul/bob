package server

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/megakuul/bob/internal/netguard"
	"github.com/megakuul/bob/internal/store"
)

// Modes of bob-server.
const (
	ModeOrchestrator = "orchestrator"
	ModeWorker       = "worker"
)

// Config is the process configuration of bob-server, read from BOB_<UPPER_SNAKE> environment
// variables (SPEC §05.11.1). Settings only a controller or the worker loop needs are read by
// that code from Env.Getenv.
type Config struct {
	Mode     string
	Name     string // BOB_SERVER_NAME: this process's id (orchestrator id); default host name
	StateDir string // BOB_STATE_DIR: local state; may be empty
	Storage  store.StorageConfig

	// Orchestrator.
	Listen      string // server.listen: BOB_SERVER_LISTEN, or BOB_LISTEN (harness contract); default :8443
	AdminListen string // server.admin_listen: BOB_SERVER_ADMIN_LISTEN; default 127.0.0.1:9090; "off" disables
	BaseURL     string // server.base_url: BOB_SERVER_BASE_URL; optional
	// AdminToken (BOB_ADMIN_TOKEN) is the bootstrap API credential (admin@*) of the test
	// stacks; only -tags bobtest builds accept it, release builds refuse to start (D-IB3-3).
	AdminToken string
	// OutboundAllow (server.outbound_allow: BOB_SERVER_OUTBOUND_ALLOW, comma-separated) names the
	// hosts, addresses and CIDR ranges outgoing forge and webhook requests may reach although
	// they are loopback, private or link-local (internal/netguard; refused by default).
	OutboundAllow []string

	// Worker.
	OrchestratorAddrs []string // BOB_ORCHESTRATOR_ADDRS: comma-separated http(s) URLs
	Slots             int      // worker.slots: BOB_WORKER_SLOTS; default max(1, nproc/2)
}

// ConfigError is a configuration that cannot work; bob-server refuses to start (exit 78).
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

func configErr(format string, a ...any) error { return &ConfigError{Msg: fmt.Sprintf(format, a...)} }

// minAdminToken is the shortest bootstrap token accepted (128 bits as hex).
const minAdminToken = 32

// ConfigFromEnv reads the configuration of mode. Every problem is a *ConfigError.
func ConfigFromEnv(mode string, getenv func(string) string) (Config, error) {
	if mode != ModeOrchestrator && mode != ModeWorker {
		return Config{}, configErr("unknown mode %q (orchestrator, worker)", mode)
	}
	or := func(def string, keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(getenv(k)); v != "" {
				return v
			}
		}
		return def
	}
	c := Config{Mode: mode, Name: getenv("BOB_SERVER_NAME"), StateDir: getenv("BOB_STATE_DIR")}
	if c.Name == "" {
		h, err := os.Hostname()
		if err != nil {
			return c, configErr("BOB_SERVER_NAME is not set and the host name is unknown: %v", err)
		}
		c.Name = h
	}
	st, err := store.StorageConfigFromEnv(getenv)
	if err != nil {
		return c, &ConfigError{Msg: err.Error()}
	}
	c.Storage = st

	switch mode {
	case ModeOrchestrator:
		c.Listen = or(":8443", "BOB_SERVER_LISTEN", "BOB_LISTEN")
		c.AdminListen = or("127.0.0.1:9090", "BOB_SERVER_ADMIN_LISTEN")
		c.BaseURL = or("", "BOB_SERVER_BASE_URL")
		if c.BaseURL != "" {
			if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return c, configErr("server.base_url %q is not an http(s) URL", c.BaseURL)
			}
		}
		c.AdminToken = getenv("BOB_ADMIN_TOKEN")
		if c.AdminToken != "" && !bootstrapToken {
			return c, configErr("BOB_ADMIN_TOKEN is honoured only by test builds (-tags bobtest); unset it and create the first admin with `bob-server admin create-user --login <name> --admin`")
		}
		if c.AdminToken != "" && len(c.AdminToken) < minAdminToken {
			return c, configErr("BOB_ADMIN_TOKEN must have at least %d characters", minAdminToken)
		}
		for _, a := range strings.Split(getenv("BOB_SERVER_OUTBOUND_ALLOW"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				c.OutboundAllow = append(c.OutboundAllow, a)
			}
		}
		if _, err := netguard.New(c.OutboundAllow); err != nil {
			return c, configErr("server.outbound_allow (BOB_SERVER_OUTBOUND_ALLOW): %v", err)
		}
	case ModeWorker:
		for _, a := range strings.Split(getenv("BOB_ORCHESTRATOR_ADDRS"), ",") {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			u, err := url.Parse(a)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return c, configErr("BOB_ORCHESTRATOR_ADDRS: %q is not an http(s) URL", a)
			}
			c.OrchestratorAddrs = append(c.OrchestratorAddrs, a)
		}
		c.Slots = max(1, runtime.NumCPU()/2)
		if v := getenv("BOB_WORKER_SLOTS"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 1024 {
				return c, configErr("worker.slots (BOB_WORKER_SLOTS) must be a number from 1 to 1024, got %q", v)
			}
			c.Slots = n
		}
	}
	return c, nil
}

// IsConfigError reports whether err is a configuration refusal.
func IsConfigError(err error) bool {
	var ce *ConfigError
	return errors.As(err, &ce)
}
