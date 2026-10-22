package evalbox

import "syscall"

func other() {
	syscall.Exit(1) // want `syscall.Exit outside func main`
}
