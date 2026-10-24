// apistub: stand-in for the fixture api on amd64 (no cross toolchain wired into B yet).
package main

import (
	"fmt"
	"runtime"
)

func main() { fmt.Printf("apistub %s/%s\n", runtime.GOOS, runtime.GOARCH) }
