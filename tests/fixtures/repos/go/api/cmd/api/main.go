// Command api combines a cgo package, generated protobuf code, an embedded file and a
// package from another workspace module.
package main

import (
	_ "embed"
	"fmt"
	"strings"

	"example.com/api/internal/ffi"
	"example.com/api/internal/msg"
	"example.com/lib/strutil"
)

//go:embed banner.txt
var banner string

var version = "dev"

func main() {
	body := strutil.Title(strings.TrimSpace(banner))
	n := msg.New(body, ffi.Checksum(body))
	fmt.Printf("%s version=%s numhex=%s note_bytes=%d\n", body, version, ffi.NumHex(42), msg.Size(n))
}
