// Command s15cache serves a file:// binary cache directory over HTTP with injectable faults.
//
//	s15cache -dir D -addr 127.0.0.1:21501 [-auth user:pass] [-mode ok|stall|nar404|corrupt|500|stall-nar]
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", "", "cache directory")
	addr := flag.String("addr", "127.0.0.1:21501", "listen address")
	auth := flag.String("auth", "", "user:pass required (basic auth)")
	mode := flag.String("mode", "ok", "ok|stall|nar404|corrupt|500|stall-nar")
	flag.Parse()
	fs := http.FileServer(http.Dir(*dir))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read only", http.StatusMethodNotAllowed)
			return
		}
		if *auth != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u+":"+p != *auth {
				w.Header().Set("WWW-Authenticate", `Basic realm="s15"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		isNar := strings.HasPrefix(r.URL.Path, "/nar/")
		switch {
		case *mode == "stall", *mode == "stall-nar" && isNar:
			time.Sleep(10 * time.Minute)
			return
		case *mode == "500":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case *mode == "nar404" && isNar:
			http.NotFound(w, r)
			return
		case *mode == "corrupt" && isNar:
			b, err := os.ReadFile(filepath.Join(*dir, filepath.Clean(r.URL.Path)))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if len(b) > 200 {
				b[len(b)-100] ^= 0xff
			}
			_, _ = w.Write(b)
			return
		}
		fs.ServeHTTP(w, r)
	})
	log.Fatal(http.ListenAndServe(*addr, h))
}
