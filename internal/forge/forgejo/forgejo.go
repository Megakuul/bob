// Package forgejo is the forgejo adapter of internal/forge.
package forgejo

import (
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Config configures one forgejo integration.
type Config struct {
	BaseURL, BotLogin          string
	BotPassword, WebhookSecret forge.Secret
}

// New builds the adapter. It refuses a non-https base URL.
func New(cfg Config) (forge.Adapter, error) {
	return nil, diag.NotImplemented("forge/forgejo.New")
}
