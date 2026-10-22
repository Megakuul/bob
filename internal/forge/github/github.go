// Package github is the github adapter of internal/forge.
package github

import (
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Config configures one github integration.
type Config struct {
	BaseURL                      string
	AppID, InstallationID        int64
	PrivateKeyPEM, WebhookSecret forge.Secret
}

// New builds the adapter. It refuses a non-https base URL.
func New(cfg Config) (forge.Adapter, error) {
	return nil, diag.NotImplemented("forge/github.New")
}
