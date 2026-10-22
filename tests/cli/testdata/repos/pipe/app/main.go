// Command app prints a greeting.
package main

import "fmt"

func main() { fmt.Println(Greeting()) }

// Greeting returns the greeting.
func Greeting() string { return "hello" }
