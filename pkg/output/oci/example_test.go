package oci_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/oci"
)

// An image of a Go server for both platforms (the default), exposing 8080 and running as
// nobody; CA certificates and time zones are added unless switched off.
func ExampleNewImage() {
	module := bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/api", Go: "1.26"})
	})
	_ = bob.Define(func(p *bob.P) *oci.Image {
		return oci.NewImage(p, "api-image", oci.ImageOpts{
			Program: module(p).Binary("./cmd/api", golang.BinaryOpts{}),
			Ports:   []int{8080},
			Env:     map[string]string{"API_ADDR": ":8080"},
		})
	})
}
