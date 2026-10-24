// Command hello prints its arguments.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() { fmt.Println("hello " + strings.Join(os.Args[1:], " ")) }
