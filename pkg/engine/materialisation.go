package engine

import "github.com/megakuul/bob/pkg/engine/ir"

// Materialisation is a planned output an ecosystem puts into the working tree (SPEC §03.6.6,
// R-FW-007): codegen output directories, an npm importer's node_modules (§03.4.3). After a
// local bob build, bob test or bob run, bob mirrors every valid one into Dir with the manifest
// protocol of pkg/engine/materialise; bob gen builds them first. Remote workers and pipeline
// runs never materialise.
type Materialisation struct {
	Dir    string // repo-relative owned directory ("web/node_modules")
	Label  string // the package whose output it is (bob gen patterns select by it)
	Drv    string // the derivation that builds Output
	Output string // store path of the tree that becomes Dir's content
	// Keep are top-level names inside Dir that belong to the user's tools and survive
	// (node_modules/.vite, node_modules/.cache).
	Keep []string
	// Record is a repo-relative file that receives Output's store path once Dir is up to date
	// (an npm importer's .bob/node_modules.path); "" for none.
	Record string
}

// Materialiser is implemented by an Ecosystem whose Plan returns Materialisations (in
// PlanResult.Materialise). bob gen plans and builds the graph only when one of its ecosystems
// reports that g has something to materialise.
type Materialiser interface {
	Materialises(g *ir.Graph) bool
}
