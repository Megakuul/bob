// Command s17 is the S17 spike: optional orchestrator peering for live build logs.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s17 orch|bench [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "orch":
		err = runOrch(os.Args[2:])
	case "bench":
		err = runBench(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "s17:", err)
		os.Exit(1)
	}
}
