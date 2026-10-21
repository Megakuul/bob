package main

import "os"

func main() { os.Exit(0) }

func helper() {
	os.Exit(1) // want `os.Exit outside func main`
}
