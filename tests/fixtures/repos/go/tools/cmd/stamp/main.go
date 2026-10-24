// Command stamp prints an embedded stamp, title-cased by example.com/lib.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"example.com/lib/fastadd"
	"example.com/lib/strutil"
	"golang.org/x/sync/errgroup"
)

//go:embed stamp.txt
var stamp string

func render() string {
	var n int64
	g, _ := errgroup.WithContext(context.Background())
	g.Go(func() error { n = fastadd.Add(40, 2); return nil })
	_ = g.Wait()
	return fmt.Sprintf("%s (%d)", strutil.Title(strings.TrimSpace(stamp)), n)
}

func main() { fmt.Println(render()) }
