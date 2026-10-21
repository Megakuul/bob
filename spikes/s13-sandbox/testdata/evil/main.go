// Hostile "bob.go" stand-in for S13. Each case tries one attack and prints PASS (contained) or
// LEAK (attack worked). The graph is written to fd 3 as a real evaluator would.
package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func report(name string, contained bool, detail string) {
	tag := "PASS"
	if !contained {
		tag = "LEAK"
	}
	fmt.Printf("%s %-28s %s\n", tag, name, detail)
}

func tryRead(name, p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		report(name, true, err.Error())
		return
	}
	report(name, false, fmt.Sprintf("read %d bytes: %.40q", len(b), b))
}

func tryDial(name, network, addr string) {
	c, err := net.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		report(name, true, err.Error())
		return
	}
	c.Close()
	report(name, false, "connected to "+addr)
}

func main() {
	mode := "all"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "ok":
		g := os.NewFile(3, "graph")
		fmt.Fprintf(g, `{"version":1,"packages":[{"label":"//x:y","kind":"go_binary"}]}`)
		g.Close()
		fmt.Println("graph written")
	case "all":
		secrets()
		network()
		escape()
	case "secrets":
		secrets()
	case "network":
		network()
	case "escape":
		escape()
	case "forkbomb-go":
		// Each process starts itself twice, forever.
		for {
			c := exec.Command("/proc/self/exe", "forkbomb-go")
			if err := c.Start(); err != nil {
				fmt.Println("start failed:", err)
				os.Exit(1)
			}
		}
	case "forkbomb-sh":
		sh := os.Getenv("BOB_BASH")
		err := syscall.Exec(sh, []string{"bash", "-c", "bomb(){ bomb | bomb & }; bomb; read -t 20 <> <(:)"}, os.Environ())
		fmt.Println("exec bash:", err)
	case "threadbomb":
		for i := 0; ; i++ {
			go func() { runtimeLock(); select {} }()
			if i%1000 == 0 {
				fmt.Println("threads", i)
			}
		}
	case "membomb":
		var keep [][]byte
		for i := 0; ; i++ {
			b := make([]byte, 64<<20)
			for j := 0; j < len(b); j += 4096 {
				b[j] = 1
			}
			keep = append(keep, b)
			fmt.Printf("allocated %d MiB\n", (i+1)*64)
		}
	case "cpuloop":
		for {
		}
	case "diskfill":
		f, err := os.Create("/work/tmp/fill")
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		buf := bytes.Repeat([]byte{1}, 1<<20)
		n := 0
		for {
			if _, err := f.Write(buf); err != nil {
				fmt.Printf("write failed after %d MiB: %v\n", n, err)
				os.Exit(1)
			}
			n++
		}
	case "bigoutput":
		g := os.NewFile(3, "graph")
		buf := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 200; i++ {
			if _, err := g.Write(buf); err != nil {
				fmt.Println("graph write failed:", err)
				os.Exit(1)
			}
		}
	}
}

func secrets() {
	tryRead("worker credentials file", "/home/bobtest/.config/bob-worker/credentials")
	tryRead("root's files", "/root/.bash_history")
	tryRead("/etc/shadow", "/etc/shadow")
	tryRead("host /etc/hostname", "/etc/hostname")
	tryRead("parent env (/proc/1/environ)", "/proc/1/environ")
	env := strings.Join(os.Environ(), " ")
	report("env has worker secrets", !strings.Contains(env, "AWS_") && !strings.Contains(env, "BOB_WORKER"), fmt.Sprintf("%d vars", len(os.Environ())))
	ents, _ := os.ReadDir("/proc")
	n := 0
	for _, e := range ents {
		if e.Name()[0] >= '0' && e.Name()[0] <= '9' {
			n++
		}
	}
	report("host processes visible", n < 10, fmt.Sprintf("%d pids in /proc", n))
	top, err := os.ReadDir("/")
	if err != nil {
		fmt.Println("INFO readdir / :", err)
	}
	var names []string
	for _, e := range top {
		names = append(names, e.Name())
	}
	report("root dir contents", !contains(names, "home") && !contains(names, "root"), strings.Join(names, ","))
	_, err = os.Stat("/run/user/1000")
	report("user runtime dir (dbus, sockets)", err != nil, fmt.Sprint(err))
	_, err = os.Stat("/sys/fs/cgroup")
	report("/sys visible", err != nil, fmt.Sprint(err))
}

func network() {
	tryDial("internet tcp 1.1.1.1:443", "tcp", "1.1.1.1:443")
	tryDial("host loopback 127.0.0.1:21300", "tcp", "127.0.0.1:21300")
	tryDial("cloud metadata 169.254.169.254", "tcp", "169.254.169.254:80")
	_, err := net.LookupHost("example.com")
	report("DNS lookup", err != nil, fmt.Sprint(err))
	tryDial("host abstract unix @s13-bait", "unix", "@s13-bait")
	tryDial("host unix socket path", "unix", "/home/bobtest/s13-bait.sock")
	ifs, _ := net.Interfaces()
	var names []string
	for _, i := range ifs {
		names = append(names, fmt.Sprintf("%s(%v)", i.Name, i.Flags&net.FlagUp != 0))
	}
	report("interfaces", len(ifs) <= 1, strings.Join(names, ","))
	// Even loopback inside: Landlock denies TCP bind.
	for _, a := range []string{"127.0.0.1:0", "127.0.0.1:8080"} {
		l, err := net.Listen("tcp", a)
		if err == nil {
			l.Close()
		}
		fmt.Printf("INFO tcp listen %s inside (private netns, lo down): %v\n", a, err)
	}
	fd, err := syscall.Socket(40 /*AF_VSOCK*/, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.Close(fd)
	}
	report("AF_VSOCK socket", err != nil, fmt.Sprint(err))
}

func escape() {
	err := syscall.PtraceAttach(1)
	report("ptrace pid 1 (stage1)", err != nil, fmt.Sprint(err))
	err = syscall.Unshare(syscall.CLONE_NEWUSER)
	report("unshare(CLONE_NEWUSER)", err != nil, fmt.Sprint(err))
	c := exec.Command("/proc/self/exe", "ok")
	c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	err = c.Run()
	report("child in new user namespace", err != nil, fmt.Sprint(err))
	err = syscall.Mount("tmpfs", "/work/tmp", "tmpfs", 0, "")
	report("mount tmpfs", err != nil, fmt.Sprint(err))
	// Rootless store: files are owned by our kernel uid. chmod+write must still fail (ro mount).
	gobin := filepath.Join(os.Getenv("GOROOT"), "bin", "go")
	err = os.Chmod(gobin, 0o755)
	report("chmod store file", err != nil, fmt.Sprint(err))
	f, err := os.OpenFile(gobin, os.O_WRONLY, 0)
	if err == nil {
		f.Close()
	}
	report("open store file for write", err != nil, fmt.Sprint(err))
	err = os.WriteFile("/src/injected", []byte("x"), 0o644)
	report("write source tree", err != nil, fmt.Sprint(err))
	f, err = os.OpenFile("/proc/1/exe", os.O_WRONLY, 0)
	if err == nil {
		f.Close()
	}
	report("open /proc/1/exe for write", err != nil, fmt.Sprint(err))
	_, err = os.Readlink("/proc/1/exe")
	report("readlink /proc/1/exe", err != nil, fmt.Sprint(err))
	err = syscall.Kill(-1, 0)
	report("signal outside pid ns (kill -1)", true, fmt.Sprint("kill(-1,0): ", err, " (only sandbox pids exist)"))
	_, _, e := syscall.Syscall(425 /*io_uring_setup arm64+amd64*/, 1, 0, 0)
	report("io_uring_setup", e != 0, e.Error())
	_, _, e = syscall.Syscall(280 /*bpf arm64*/, 0, 0, 0)
	report("bpf()", e != 0, e.Error())
	err = os.WriteFile("/proc/sys/kernel/hostname", []byte("x"), 0)
	report("write /proc/sys", err != nil, fmt.Sprint(err))
	err = os.Symlink("/", "/work/tmp/l")
	report("symlink in /work (allowed)", true, fmt.Sprint(err))
	fmt.Printf("INFO uid=%d euid=%d caps: %s\n", os.Getuid(), os.Geteuid(), capLine())
}

func capLine() string {
	b, _ := os.ReadFile("/proc/self/status")
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "CapEff") || strings.HasPrefix(l, "Seccomp:") || strings.HasPrefix(l, "NoNewPrivs") {
			out = append(out, strings.Join(strings.Fields(l), " "))
		}
	}
	return strings.Join(out, "; ")
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
