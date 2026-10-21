package main

import (
	"os"
	"path/filepath"

	"bobspike/s06/cg"
)

// define is the spike's bob.go: what a developer would write (see the SPEC proposal in
// docs/spikes/s06-codegen.md for the real API shape).
func define(b *cg.B, spike string) error {
	common := b.ProtoLibrary("proto", "common/v1")
	greet := b.ProtoLibrary("proto", "greet/v1", common)
	billing := b.ProtoLibrary("proto", "billing/v1")

	svc, err := cg.NewGoModule(b, "svc", "example.com/svc")
	if err != nil {
		return err
	}
	svc.ConnectGo(greet) // also generates common/v1 (memoised)
	if os.Getenv("S06_DROP_BILLING") == "" {
		svc.ConnectGo(billing)
	}
	svc.Gqlgen("schema", "schema.graphqls")
	svc.OpenAPIGo("schema", "items.yaml", "rest")
	svc.Binaries("bin", "./cmd/server", "./cmd/client")

	webNM, err := b.NodeModulesFromLock("//web:node_modules", "web/package-lock.json")
	if err != nil {
		return err
	}
	tool := filepath.Join(spike, "tools/graphql-codegen")
	gqlTool, err := b.NodeModulesFromLock("//tools/graphql-codegen:node_modules", filepath.Join(tool, "package-lock.json"))
	if err != nil {
		return err
	}
	web := b.TSProject("web", webNM)
	web.ConnectTS(greet)
	web.GraphQLTS(gqlTool, filepath.Join(tool, "driver.mjs"), "schema", []string{"schema.graphqls"}, []string{"hello.graphql"})
	web.Build("client")
	return nil
}
