// Command selfkill kills itself with SIGKILL.
package main

import "syscall"

func main() { _ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL) }
