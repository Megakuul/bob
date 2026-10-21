// hookrecv captures webhook deliveries as fixtures: <dir>/<seq>-<event>.headers.json + .body.json.
// Body bytes are stored exactly as received so signatures stay verifiable.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:21601", "listen address")
	dir := flag.String("dir", "fixtures/forgejo/raw", "output dir")
	flag.Parse()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Print(err)
		return
	}
	var mu sync.Mutex
	seq := 0
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ev := r.Header.Get("X-Forgejo-Event")
		if ev == "" {
			ev = r.Header.Get("X-Gitlab-Event")
		}
		ev = strings.ReplaceAll(strings.ToLower(ev), " ", "_")
		if t := r.Header.Get("X-Forgejo-Event-Type"); t != "" && t != ev {
			ev += "." + t
		}
		mu.Lock()
		seq++
		n := seq
		mu.Unlock()
		base := filepath.Join(*dir, fmt.Sprintf("%03d-%s", n, ev))
		h := map[string]string{}
		for k := range r.Header {
			h[k] = r.Header.Get(k)
		}
		hb, _ := json.MarshalIndent(h, "", "  ")
		_ = os.WriteFile(base+".headers.json", hb, 0o644)
		_ = os.WriteFile(base+".body.json", body, 0o644)
		log.Printf("%s %d bytes", base, len(body))
		w.WriteHeader(204)
	})
	log.Printf("listening on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Print(err)
	}
}
