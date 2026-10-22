package evalbox

import (
	"os"
	"syscall"
)

func init() {
	if len(os.Args) > 5 {
		syscall.Exit(3)
	}
}

func stray() {
	os.Exit(1) // want `os.Exit outside func main`
}
