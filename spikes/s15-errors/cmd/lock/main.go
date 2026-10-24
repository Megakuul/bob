// Command lock downloads every fetched input of the fixture and writes lock.json (name -> SRI sha256).
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"

	"bobspike/s15/fixturegraph"
)

func main() {
	out := map[string]string{}
	for _, f := range fixturegraph.Fetches() {
		resp, err := http.Get(f.URL)
		if err != nil || resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "fetch %s: %v %v\n", f.URL, err, resp)
			os.Exit(1)
		}
		h := sha256.New()
		if _, err := io.Copy(h, resp.Body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		resp.Body.Close()
		out[f.Name] = "sha256-" + base64.StdEncoding.EncodeToString(h.Sum(nil))
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile("lock.json", append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote lock.json with", len(keys), "entries")
}
