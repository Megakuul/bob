// Command helper is the service, probe and test program of pkg/builder's test-run tests.
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() { os.Exit(helperMain(os.Args[1], os.Args[2:])) }

func helperMain(mode string, args []string) int {
	switch mode {
	case "listen": // a service: pid file, then accept on $ADDR:<port> until killed
		_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
		fmt.Println("listening on", os.Getenv("ADDR"), "argv", strings.Join(args, " "))
		l, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("ADDR"), args[0]))
		if err != nil {
			fmt.Println(err)
			return 1
		}
		for {
			c, err := l.Accept()
			if err != nil {
				return 1
			}
			c.Close()
		}
	case "sleeper": // never ready
		_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
		for n := 1; n <= 30; n++ {
			fmt.Printf("sleeper line %d\n", n)
		}
		time.Sleep(time.Hour)
	case "crash":
		fmt.Println("cannot start: config missing")
		return 3
	case "probe": // CmdReady: exit 0 once $ADDR:<port> accepts
		c, err := net.DialTimeout("tcp", net.JoinHostPort(os.Getenv("ADDR"), args[0]), time.Second)
		if err != nil {
			return 1
		}
		c.Close()
		return 0
	case "test": // a Go-style test that dials <VAR>:<port> and checks the env
		fmt.Println("=== RUN   TestDial")
		c, err := net.DialTimeout("tcp", net.JoinHostPort(os.Getenv(args[0]), args[1]), time.Second)
		if err != nil {
			fmt.Println("    dial:", err)
			fmt.Println("--- FAIL: TestDial (0.01s)")
			return 1
		}
		c.Close()
		fmt.Println("--- PASS: TestDial (0.01s)")
		fmt.Println("=== RUN   TestEnv")
		if os.Getenv("HOME") != os.Getenv("TMPDIR") || os.Getenv("DSN") != "db://"+os.Getenv("DB_ADDR")+":"+args[2] {
			fmt.Printf("    HOME=%s TMPDIR=%s DSN=%s\n", os.Getenv("HOME"), os.Getenv("TMPDIR"), os.Getenv("DSN"))
			fmt.Println("--- FAIL: TestEnv (0.00s)")
			return 1
		}
		fmt.Println("--- PASS: TestEnv (0.00s)")
		fmt.Println("PASS")
		return 0
	case "fail":
		fmt.Println("=== RUN   TestA")
		fmt.Println("--- PASS: TestA (0.00s)")
		fmt.Println("=== RUN   TestB")
		fmt.Println("    b_test.go:3: fails on purpose")
		fmt.Println("--- FAIL: TestB (0.02s)")
		fmt.Println("FAIL")
		return 1
	}
	return 2
}
