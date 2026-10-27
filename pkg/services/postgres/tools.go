package postgres

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
)

// tools are the server package, bash and coreutils as host tools.
func tools(p *bob.P, server nixext.Attr) []bob.Tool {
	return []bob.Tool{server.Tool(p), nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)}
}
