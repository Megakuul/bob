package graphql_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/npm"
)

// A schema used by a Go module (gqlgen into gen/graph) and by a Node program (graphql-codegen
// types of the schema and of its operations into gen/graphql).
func ExampleNewSchema() {
	api := bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })
	_ = bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/svc", Go: "1.26",
			Generated: []golang.Generated{graphql.Gqlgen(api(p), graphql.GqlgenOpts{})}})
	})
	_ = bob.Define(func(p *bob.P) *npm.App {
		return npm.NewApp(p, npm.AppOpts{Name: "client", Entry: "src/client.ts",
			Generated: []npm.Generated{graphql.TS(api(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")})}})
	})
}
