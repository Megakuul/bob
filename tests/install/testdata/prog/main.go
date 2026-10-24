// Command prog is the program the install tests release as "acme": --version prints its version,
// --env prints its environment, one NAME=value per line.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--env" {
		for _, e := range os.Environ() {
			fmt.Println(e)
		}
		return
	}
	fmt.Println(version)
}
