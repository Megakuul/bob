//go:build bob

package it

import (
	"example.com/svc"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/services/postgres"
)

func pg(p *bob.P) bob.Service {
	return postgres.Service(p, postgres.Options{Version: "17", Databases: []string{"app"}})
}

// Test is an integration test against Postgres (label //it:it).
var Test = bob.Define(func(p *bob.P) *golang.TestSet {
	return svc.Module(p).Tests("./it", golang.TestOpts{Name: "it", Services: []bob.Service{pg(p)}})
})

// Test2 is the same test as a second target; both run in parallel (label //it:it2).
var Test2 = bob.Define(func(p *bob.P) *golang.TestSet {
	return svc.Module(p).Tests("./it", golang.TestOpts{Name: "it2", Services: []bob.Service{pg(p)}})
})

// Never uses a service that never becomes ready (label //it:never).
var Never = bob.Define(func(p *bob.P) *golang.TestSet {
	sleeper := svc.Module(p).Binary("./cmd/sleeper", golang.BinaryOpts{})
	never := bob.ServiceFrom(sleeper, bob.ServiceOptions{Ready: bob.TCPReady(9)})
	return svc.Module(p).Tests("./it", golang.TestOpts{Name: "never", Services: []bob.Service{never}})
})
