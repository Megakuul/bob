// Command lock pins every Go module file listed in a go.sum (zip or go.mod) by the sha256 of
// the file the Go proxy serves. npm tarballs need no step here: package-lock.json already
// carries their sha512 integrity. This stands in for bob's resolver output in bob.lock.
//
//	go run ./cmd/lock fixture/svc/go.sum > lock.json
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

func escape(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 3 {
			continue
		}
		path, ver := fs[0], fs[1]
		ext := ".zip"
		if v, ok := strings.CutSuffix(ver, "/go.mod"); ok {
			ver, ext = v, ".mod"
		}
		key := path + "@" + ver + ext
		url := "https://proxy.golang.org/" + escape(path) + "/@v/" + ver + ext
		resp, err := http.Get(url)
		if err != nil || resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "fetch %s: %v %v\n", url, err, resp)
			os.Exit(1)
		}
		h := sha256.New()
		_, err = io.Copy(h, resp.Body)
		resp.Body.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out[key] = "sha256-" + base64.StdEncoding.EncodeToString(h.Sum(nil))
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
