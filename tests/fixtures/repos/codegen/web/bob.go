//go:build bob

package web

import (
	greetv1 "example.com/codegen/proto/greet/v1"
	"example.com/codegen/schema"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/npm"
)

// Client is a Node program using generated connect-es and graphql-codegen code (label //web:client).
var Client = bob.Define(func(p *bob.P) *npm.App {
	return npm.App(p, npm.AppOpts{
		Name:  "client",
		Entry: "src/client.ts",
		Deps: []npm.Dependency{
			npm.Dep("@connectrpc/connect", "^2"),
			npm.Dep("@connectrpc/connect-node", "^2"),
			npm.Dep("@types/node", "^24", npm.Dev()),
		},
		Generated: []npm.Generated{
			connect.TS(greetv1.Greet(p), connect.TSOpts{}),
			graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")}),
		},
	})
})
