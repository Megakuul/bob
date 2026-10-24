package oci_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/oci"
)

// API is the typed slot of the program, declared in an importable bob.go (acme/bob.go). The
// main package's own bob.go (cmd/api/bob.go) declares the binary and binds it:
// var _ = acme.API.Bind(Binary).
var API = bob.NewSlot[*golang.Binary]()

// An image of a Go server for both platforms (the default), exposing 8080 and running as
// nobody; CA certificates and time zones are added unless switched off.
func ExampleNewImage() {
	_ = bob.Define(func(p *bob.P) *oci.Image {
		return oci.NewImage(p, "api-image", oci.ImageOpts{
			Program: API.Get(p),
			Ports:   []int{8080},
			Env:     map[string]string{"API_ADDR": ":8080"},
		})
	})
}
