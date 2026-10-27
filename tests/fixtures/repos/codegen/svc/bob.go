//go:build bob

package svc

import (
	greetv1 "example.com/codegen/proto/greet/v1"
	"example.com/codegen/schema"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/openapi"
)

// Module is example.com/svc with three generated trees: gen/proto (connect), gen/graph
// (gqlgen) and gen/rest (oapi-codegen) (label //svc:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path: "example.com/svc",
		Go:   "1.26",
		Generated: []golang.Generated{
			connect.Go(greetv1.Greet(p), connect.GoOpts{}),
			graphql.Gqlgen(schema.API(p), graphql.GqlgenOpts{}),
			openapi.Go(schema.Items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP}),
		},
	})
})
