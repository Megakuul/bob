// Package gitlab is the gitlab adapter of internal/forge.
package gitlab

import (
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Config configures one gitlab integration.
type Config struct {
	BaseURL                   string
	AccessToken, SigningToken forge.Secret
	AllowPlaintextSecret      bool
}

// New builds the adapter. It refuses a non-https base URL.
func New(cfg Config) (forge.Adapter, error) {
	return nil, diag.NotImplemented("forge/gitlab.New")
}
