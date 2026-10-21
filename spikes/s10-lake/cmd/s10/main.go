// Command s10 runs the S10 lake experiments. Usage: s10 <experiment> [flags]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"bobspike/s10/logstore"
)

var experiments = map[string]func(ctx context.Context, args []string) error{}

func main() {
	if len(os.Args) < 2 || experiments[os.Args[1]] == nil {
		names := []string{}
		for k := range experiments {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Fprintln(os.Stderr, "usage: s10 <experiment>; experiments:", names)
		os.Exit(2)
	}
	ctx := context.Background()
	if err := experiments[os.Args[1]](ctx, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func endpointFlag(fs *flag.FlagSet) *string {
	return fs.String("s3", "seaweed", "seaweed|rustfs")
}

func endpoint(name string) logstore.Endpoint {
	if name == "rustfs" {
		return logstore.RustFS
	}
	return logstore.SeaweedFS
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(float64(len(s)-1) * p)
	return s[i]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000) }
