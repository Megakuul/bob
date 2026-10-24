package main

import "log"

func main() { log.Fatal("ok in main") }

func helper() { panic("ok in package main under cmd/") }
