// Command server serves the connect GreetService, a gqlgen GraphQL endpoint and an
// oapi-codegen REST endpoint, all from bob-generated code.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	commonv1 "example.com/svc/gen/proto/common/v1"
	greetv1 "example.com/svc/gen/proto/greet/v1"
	"example.com/svc/gen/proto/greet/v1/greetv1connect"
	gqlexec "example.com/svc/gen/graph"
	restapi "example.com/svc/gen/rest"
	"example.com/svc/graph"
	"example.com/svc/rest"
)

type greeter struct{}

func (greeter) Greet(_ context.Context, r *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	req := r.Msg
	if req.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	return connect.NewResponse(&greetv1.GreetResponse{
		Greeting: "hello " + req.GetName(),
		Meta:     &commonv1.Meta{RequestId: "r-1", UnixMillis: time.Now().UnixMilli()},
	}), nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:20600", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	path, h := greetv1connect.NewGreetServiceHandler(greeter{})
	mux.Handle(path, h)

	gql := handler.New(gqlexec.NewExecutableSchema(gqlexec.Config{Resolvers: &graph.Resolver{}}))
	gql.AddTransport(transport.POST{})
	mux.Handle("/graphql", gql)

	mux.Handle("/rest/", http.StripPrefix("/rest", restapi.Handler(rest.Server{})))

	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Addr: *addr, Handler: mux, Protocols: p}
	log.Printf("listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
