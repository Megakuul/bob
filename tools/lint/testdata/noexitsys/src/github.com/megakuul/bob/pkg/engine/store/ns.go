package store

import "syscall"

// The namespace child: syscall.Exit is allowed in this file.
func nsChild() {
	syscall.Exit(126)
}
