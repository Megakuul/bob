// Command loopsvc is S13's tiny loopback service + client for tests inside the Nix sandbox.
//
//	loopsvc serve ADDR            serve HTTP on ADDR until killed; prints "ready ADDR"
//	loopsvc probe ADDR N          wait until ADDR answers, then do N requests; print timings
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	switch os.Args[1] {
	case "serve":
		l, err := net.Listen("tcp", os.Args[2])
		if err != nil {
			fmt.Println("listen:", err)
			os.Exit(1)
		}
		fmt.Println("ready", l.Addr())
		http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "pong") }))
	case "probe":
		addr := os.Args[2]
		n, _ := strconv.Atoi(os.Args[3])
		t0 := time.Now()
		for {
			c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				c.Close()
				break
			}
			if time.Since(t0) > 10*time.Second {
				fmt.Println("not ready:", err)
				os.Exit(1)
			}
			time.Sleep(2 * time.Millisecond)
		}
		ready := time.Since(t0)
		t1 := time.Now()
		for i := 0; i < n; i++ {
			resp, err := http.Get("http://" + addr + "/")
			if err != nil {
				fmt.Println("get:", err)
				os.Exit(1)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		d := time.Since(t1)
		fmt.Printf("probe %s ready_after=%s requests=%d total=%s per_req=%s\n", addr, ready.Round(time.Microsecond), n, d.Round(time.Microsecond), (d / time.Duration(n)).Round(time.Microsecond))
	}
}
