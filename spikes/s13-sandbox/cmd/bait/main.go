// Command bait plays the worker's host side for S13: it listens on host loopback, an abstract
// unix socket and a unix socket path, and logs every connection (a connection = sandbox leak).
package main

import (
	"fmt"
	"net"
	"os"
)

func serve(l net.Listener, name string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		fmt.Println("BAIT: connection on", name)
		c.Close()
	}
}

func main() {
	tcp, err := net.Listen("tcp", "127.0.0.1:21300")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	go serve(tcp, "tcp 127.0.0.1:21300")
	abs, err := net.Listen("unix", "@s13-bait")
	if err == nil {
		go serve(abs, "abstract @s13-bait")
	}
	os.Remove(os.Args[1])
	p, err := net.Listen("unix", os.Args[1])
	if err == nil {
		go serve(p, os.Args[1])
	}
	fmt.Println("BAIT: ready")
	select {}
}
