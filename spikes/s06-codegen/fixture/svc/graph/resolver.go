// Package graph holds the hand-written GraphQL resolvers (user-owned, never generated).
package graph

import (
	"context"

	gqlexec "example.com/svc/gen/graph"
	"example.com/svc/gen/graph/model"
)

type Resolver struct{}

func (r *Resolver) Query() gqlexec.QueryResolver { return queryResolver{} }

type queryResolver struct{}

func (queryResolver) Hello(_ context.Context, name string) (*model.Hello, error) {
	g := "hello " + name
	return &model.Hello{Greeting: g, Length: len(g)}, nil
}
