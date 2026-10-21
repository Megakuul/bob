// Command authcache is a throwaway spike (S0b): serves a file:// Nix binary cache directory over
// HTTP and requires HTTP basic auth (what Nix sends from a netrc file). Logs every request.
package main

import (
	"crypto/subtle"
	"flag"
	"log"
	"net/http"
)

func main() {
	dir := flag.String("dir", "", "binary cache directory")
	addr := flag.String("addr", "127.0.0.1:8088", "listen address")
	user := flag.String("user", "bob", "basic auth user")
	pass := flag.String("pass", "", "basic auth password (token)")
	flag.Parse()
	files := http.FileServer(http.Dir(*dir))
	log.Fatal(http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(*user)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(*pass)) != 1 {
			log.Printf("401 %s %s (auth=%v)", r.Method, r.URL.Path, ok)
			w.Header().Set("WWW-Authenticate", `Basic realm="bob"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		log.Printf("200 %s %s", r.Method, r.URL.Path)
		files.ServeHTTP(w, r)
	})))
}
