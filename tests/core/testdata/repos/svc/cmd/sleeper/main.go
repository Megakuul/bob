// Command sleeper logs 30 lines and then sleeps without ever listening.
package main

import (
	"fmt"
	"time"
)

func main() {
	for i := 1; i <= 30; i++ {
		fmt.Printf("sleeper line %d\n", i)
	}
	time.Sleep(10 * time.Minute)
}
