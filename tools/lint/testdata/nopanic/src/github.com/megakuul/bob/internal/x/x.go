package x

import "log"

var _ = func() int { panic("x") }() // want `panic outside package main`

func bad() {
	panic("boom")    // want `panic outside package main`
	log.Fatalf("x")  // want `log.Fatalf outside package main`
	log.Panicln("x") // want `log.Panicln outside package main`
	log.Printf("fine")
}
