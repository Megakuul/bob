package postgres

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// tools are the programs of the start script and the probe: the postgresql package attr,
// bash and coreutils (sleep) from nixpkgs.
func tools(p *bob.P, attr string) []bob.Tool {
	return []bob.Tool{nixpkgs.Tool(p, attr), nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")}
}
