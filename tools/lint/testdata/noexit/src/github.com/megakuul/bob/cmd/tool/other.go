package main

import "os"

func init() {
	os.Exit(2) // want `os.Exit outside func main`
}
