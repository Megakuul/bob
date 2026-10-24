package postgres_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/services/postgres"
)

// A test unit that needs a PostgreSQL 17 server with the database "app" lists the service in
// its ecosystem's TestOpts.Services; the test reads DATABASE_URL.
func ExampleService() {
	_ = bob.Define(func(p *bob.P) []bob.Service {
		return []bob.Service{postgres.Service(p, postgres.Options{Server: nixpkgs.Postgresql17, Databases: []string{"app"}})}
	})
}
