package gen

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
)

// PrepareDev puts the pinned nodejs of the repository's npm importers on the dev shell's PATH
// (R-DEV-001) and the commands of their direct dependencies in the materialised
// node_modules/.bin as in-tree tools (tsc, vite, vitest: shims, R-NPM-008a). node_modules
// itself is materialised by bob gen and local builds (§03.4.3); the Vite dev server is the bob
// dev contract of §03.4.8 (M9.6).
func (Ecosystem) PrepareDev(_ context.Context, req engine.DevRequest) (engine.DevPrep, error) {
	var prep engine.DevPrep
	if req.Graph == nil {
		return prep, nil
	}
	ims, _, err := importers(req.Graph)
	if err != nil {
		return prep, err
	}
	prep.Tools = binTools(ims, req.Lock)
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

// toolName reports whether cmd is a command name bob makes a shim for ([A-Za-z0-9][A-Za-z0-9._+-]*;
// no regexp: package init stays cheap for the shell hook).
func toolName(cmd string) bool {
	for i, c := range cmd {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._+-", c)) {
			return false
		}
	}
	return cmd != "" && len(cmd) <= 128
}

// binTools maps every command of the importers' direct dependencies to its .bin wrapper in the
// importer's node_modules (repo-relative); the first importer by directory wins a name.
func binTools(ims []*importer, raw json.RawMessage) map[string]string {
	sec, err := decodeSection(raw)
	if err != nil || sec == nil {
		return nil
	}
	out := map[string]string{}
	for _, im := range ims {
		rec, ok := sec.Importers[im.dir]
		if !ok {
			continue
		}
		for _, alias := range sortedKeys(rec.Deps) {
			p, ok := sec.Packages[rec.Deps[alias]]
			if !ok {
				continue
			}
			for _, cmd := range sortedKeys(p.Bin) {
				if _, taken := out[cmd]; !taken && toolName(cmd) {
					out[cmd] = path.Join(im.dir, "node_modules", ".bin", cmd)
				}
			}
		}
	}
	return out
}
