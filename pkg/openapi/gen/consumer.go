package gen

import (
	"fmt"
	"path"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// ownedDir is the repository-relative directory a codegen node's output becomes: dir (from
// the recipe) below its consumer, whose directory is the node's label directory (§03.6.1). It
// needs no consumer node, so `bob build //svc:gen-graph` plans the same derivation as a build
// of the consumer.
func ownedDir(n *ir.Node, dir string) (string, error) {
	owned, err := engine.OwnedDir(engine.LabelDir(n.Label), path.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("%s: %w", n.Label, err)
	}
	return owned, nil
}
