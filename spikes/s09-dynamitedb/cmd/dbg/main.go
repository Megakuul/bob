// Command dbg is a small LIST check used while debugging the spike.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"bobspike/s09/internal/store"
)

func main() {
	if os.Args[2] == "filedir" {
		fileDir(os.Args[1])
		return
	}
	if os.Args[2] == "dir" {
		dirMarker(os.Args[1])
		return
	}
	if os.Args[2] == "rw" {
		readDuringWrite(os.Args[1], false)
		readDuringWrite(os.Args[1], true)
		return
	}
	ctx := context.Background()
	c := store.Open(os.Args[1], os.Args[2], "bobadmin", "bobadmin-secret", nil, 1)
	for _, p := range os.Args[3:] {
		for _, lim := range []int{0, 50} {
			t := time.Now()
			objs, err := c.ListObjects(ctx, p, lim)
			fmt.Println(p, lim, len(objs), err, time.Since(t))
		}
	}
}
