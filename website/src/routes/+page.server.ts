import { highlight } from '$lib/server/highlight';
import type { PageServerLoad } from './$types';

// The front page's bob.go sample; the API calls are the ones in pkg/golang and pkg/output/oci
// examples (example_test.go), so the sample stays honest.
const sample = `//go:build bob

package api

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/oci"
)

var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path: "example.com/api",
		Go:   "1.26",
	})
})

var Server = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/api", golang.BinaryOpts{})
})

// amd64 and arm64 images, layered by store path, no Dockerfile.
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "api", oci.ImageOpts{
		Program: Server(p),
		Ports:   []int{8080},
	})
})
`;

export const load: PageServerLoad = () => ({ sample: { code: sample, html: highlight(sample, 'go') } });
