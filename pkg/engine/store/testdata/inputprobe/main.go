// Command inputprobe runs inside a bob derivation and fails when the build can change one of
// its inputs: chmod or create a file in every store path it sees (directly and through the
// /proc/1/root of the sandbox's first process), or runs as root. On success it writes $out/ok.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := os.Getenv("out")
	if out == "" || len(os.Args) != 2 {
		fmt.Println("usage: $out=<output> inputprobe <store dir>")
		os.Exit(2)
	}
	store := os.Args[1]
	bad := 0
	if os.Getuid() == 0 {
		fmt.Println("probe: runs as root")
		bad++
	}
	entries, err := os.ReadDir(store)
	if err != nil || len(entries) == 0 {
		fmt.Printf("probe: no store entries visible: %v\n", err)
		os.Exit(2)
	}
	for _, e := range entries {
		for _, root := range []string{"", "/proc/1/root"} {
			p := root + filepath.Join(store, e.Name())
			if p == out {
				continue
			}
			if err := os.Chmod(p, 0o777); err == nil {
				fmt.Printf("probe: chmod %s succeeded\n", p)
				bad++
			}
			if e.IsDir() {
				if err := os.WriteFile(filepath.Join(p, ".probe"), []byte("x"), 0o644); err == nil {
					fmt.Printf("probe: write into %s succeeded\n", p)
					bad++
				}
			}
		}
	}
	if bad > 0 {
		os.Exit(1)
	}
	if err := os.Mkdir(out, 0o755); err != nil {
		fmt.Println("probe:", err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(out, "ok"), []byte("ok\n"), 0o644); err != nil {
		fmt.Println("probe:", err)
		os.Exit(2)
	}
}
