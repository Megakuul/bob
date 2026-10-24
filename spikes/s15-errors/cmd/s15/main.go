// Command s15 captures Nix internal-json streams of B-style builds (fault fixtures) and
// replays them through the translator.
//
//	s15 capture -name N -nix 2.34.9 -graph G [-target L] [-nixarg ARG]... [-store URI]
//	            [-sigint 5s] [-wrap "systemd-run …"] [-inject attr=EXPR] [-out fixtures]
//	s15 replay  [-json] [-v] [-color] DIR
//	s15 types   fixtures            (catalogue of message/activity/result types)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, " ") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s15 capture|replay|types …")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "capture":
		err = cmdCapture(os.Args[2:])
	case "replay":
		var code int
		code, err = cmdReplay(os.Args[2:])
		if err == nil {
			os.Exit(code)
		}
	case "types":
		err = cmdTypes(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "s15:", err)
		os.Exit(1)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var _ = flag.ContinueOnError
