package main

import "syscall"

func main() { syscall.Exit(0) }

func helper() {
	syscall.Exit(1) // want `syscall.Exit outside func main`
}
