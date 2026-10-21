//go:build bob

package schema

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/openapi"
)

// API is the GraphQL schema.
var API = bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })

// Items is the OpenAPI spec.
var Items = bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })
