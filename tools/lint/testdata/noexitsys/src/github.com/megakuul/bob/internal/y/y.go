package y

import "syscall"

func bad() {
	syscall.Exit(1) // want `syscall.Exit outside func main`
}
