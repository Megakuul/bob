package openapi_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/npm"
	"github.com/megakuul/bob/pkg/openapi"
)

// An OpenAPI document served by a Go module (oapi-codegen net/http server and models into
// gen/rest) and called from a Node program (openapi-typescript types into gen/openapi).
func ExampleNewSpec() {
	items := bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })
	_ = bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/svc", Go: "1.26",
			Generated: []golang.Generated{openapi.Go(items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP})}})
	})
	_ = bob.Define(func(p *bob.P) *npm.App {
		return npm.NewApp(p, npm.AppOpts{Name: "client", Entry: "src/client.ts",
			Generated: []npm.Generated{openapi.TS(items(p), openapi.TSOpts{})}})
	})
}
