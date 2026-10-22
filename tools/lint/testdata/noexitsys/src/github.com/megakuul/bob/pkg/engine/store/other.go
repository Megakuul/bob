package store

import (
	"os"
	"syscall"
)

func helper() {
	syscall.Exit(1) // want `syscall.Exit outside func main`
	os.Exit(1)      // want `os.Exit outside func main`
}
