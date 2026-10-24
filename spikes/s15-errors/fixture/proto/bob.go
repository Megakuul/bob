//go:build ignore

// Illustrative bob.go (S15).
package proto

import "github.com/megakuul/bob/pkg/gen/protobuf"

var Greeting = protobuf.Go("greeting_go", protobuf.Opts{
	Files:     []string{"greeting.proto"},
	GoPackage: "example.com/api/gen/greetingv1",
})
