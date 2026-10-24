package connect

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/connect/internal/connspec"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/npm"
	"github.com/megakuul/bob/pkg/proto"
)

// The generators run on the host and produce the same files for every target platform.
func init() {
	ext.Register(ext.Kind{Name: connspec.KindGo})
	ext.Register(ext.Kind{Name: connspec.KindTS})
}

// GoOpts configures connect.Go.
type GoOpts struct {
	Dir    string // owned dir relative to the module; default "gen/proto/<proto dir relative to Root>"
	Simple bool   // connect-go simple API
}

// Go generates protoc-gen-go + protoc-gen-connect-go code for lib and every library it imports
// (each into its default directory, gen/proto/<dir>). Adds google.golang.org/protobuf and
// connectrpc.com/connect at the generator versions of bob's nixpkgs pin (§03.6.4).
//
//	golang.ModuleOpts{Generated: []golang.Generated{connect.Go(greetv1.Greet(p), connect.GoOpts{})}}
func Go(lib *proto.Library, o GoOpts) golang.Generated {
	return goRecipe(lib, o, ext.Here())
}

func goRecipe(lib *proto.Library, o GoOpts, at ext.Site) golang.Generated {
	if lib == nil {
		ext.Errorf("connect.Go: the library is nil")
		return golang.Generated{}
	}
	dir := o.Dir
	if dir == "" {
		dir = defaultDir(lib)
	}
	imports := map[string]string{}
	for _, l := range lib.Closure() {
		imports[libDir(l)] = defaultDir(l)
	}
	imports[libDir(lib)] = dir
	g := golang.Generated{
		Kind:  connspec.KindGo,
		Dir:   dir,
		Deps:  []bob.Package{lib},
		Tools: []string{"buf", "protoc-gen-go", "protoc-gen-connect-go"},
		Spec:  connspec.Gen{Dir: dir, Simple: o.Simple, Imports: imports},
		Require: []golang.Requirement{
			golang.GeneratorRuntime("google.golang.org/protobuf", "protoc-gen-go", false),
			golang.GeneratorRuntime("connectrpc.com/connect", "protoc-gen-connect-go", false),
		},
		At: at,
	}
	for _, d := range lib.Imports() {
		g.Also = append(g.Also, goRecipe(d, GoOpts{Simple: o.Simple}, at))
	}
	return g
}

// TSOpts configures connect.TS.
type TSOpts struct{ Dir string } // default "gen/proto/<proto dir>"

// TS generates protoc-gen-es (target=ts, import_extension=js) code for lib and every library it
// imports; adds @bufbuild/protobuf at the generator version. @connectrpc/connect(-web|-node)
// are ordinary npm deps.
//
//	npm.ViteOpts{Generated: []npm.Generated{connect.TS(greetv1.Greet(p), connect.TSOpts{})}}
func TS(lib *proto.Library, o TSOpts) npm.Generated {
	return tsRecipe(lib, o, ext.Here())
}

func tsRecipe(lib *proto.Library, o TSOpts, at ext.Site) npm.Generated {
	if lib == nil {
		ext.Errorf("connect.TS: the library is nil")
		return npm.Generated{}
	}
	dir := o.Dir
	if dir == "" {
		dir = defaultDir(lib)
	}
	g := npm.Generated{
		Kind:    connspec.KindTS,
		Dir:     dir,
		Deps:    []bob.Package{lib},
		Tools:   []string{"buf", "protoc-gen-es"},
		Spec:    connspec.Gen{Dir: dir},
		Runtime: []npm.GenRuntime{{Name: "@bufbuild/protobuf", Generator: "protoc-gen-es"}},
		At:      at,
	}
	for _, d := range lib.Imports() {
		g.Also = append(g.Also, tsRecipe(d, TSOpts{}, at))
	}
	return g
}

// libDir is a library's repo-relative directory.
func libDir(l *proto.Library) string { return l.Label().Dir }

// defaultDir is gen/proto/<library dir relative to its import root>.
func defaultDir(lib *proto.Library) string {
	if d := lib.Dir(); d != "" {
		return "gen/proto/" + d
	}
	return "gen/proto"
}
