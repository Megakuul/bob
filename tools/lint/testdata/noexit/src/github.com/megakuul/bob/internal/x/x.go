package x

import "os"

func bad() {
	os.Exit(1) // want `os.Exit outside func main`
}
