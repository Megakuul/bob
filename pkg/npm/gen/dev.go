package gen

import (
	"context"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
)

// PrepareDev puts the pinned nodejs of the repository's npm importers on the dev shell's PATH
// (R-DEV-001). Materialising node_modules into the working tree and the Vite dev server are
// the bob dev contract of §03.4.8 (M9.6).
func (Ecosystem) PrepareDev(_ context.Context, req engine.DevRequest) (engine.DevPrep, error) {
	var prep engine.DevPrep
	if req.Graph == nil {
		return prep, nil
	}
	ims, _, err := importers(req.Graph)
	if err != nil {
		return prep, err
	}
	for _, im := range ims {
		i := im.spec.Tools.Node
		if i < 0 || i >= len(im.node.Deps) {
			continue
		}
		rv, ok := req.Inputs[im.node.Deps[i]]
		if !ok || rv.Outputs["out"] == "" {
			continue
		}
		out := rv.Outputs["out"]
		version := out[strings.LastIndex(out, "-")+1:]
		prep.Packages = append(prep.Packages, engine.DevPackage{Name: "node", Version: version, Path: out, Drv: rv.Drv})
		return prep, nil // one nodejs per repository (the pinned one)
	}
	return prep, nil
}
