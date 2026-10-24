package main

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Seccomp deny-list in classic BPF, built in pure Go (no libseccomp, no cgo). It removes kernel
// attack surface a build definition never needs: namespaces, mounts, ptrace, bpf, io_uring,
// keyrings, perf, userfaultfd, module/kexec, clock changes, and all socket families except
// unix/inet/inet6 (AF_VSOCK is not network-namespaced).

const (
	offNr   = 0
	offArch = 4
	offArg0 = 16 // low 32 bits of args[0] on little-endian
)

func auditArch() (uint32, error) {
	switch runtime.GOARCH {
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	}
	return 0, fmt.Errorf("unsupported arch %s", runtime.GOARCH)
}

var deniedSyscalls = []uintptr{
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD,
	unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT, unix.SYS_MOUNT_SETATTR,
	unix.SYS_MOVE_MOUNT, unix.SYS_OPEN_TREE, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK,
	unix.SYS_UNSHARE, unix.SYS_SETNS,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
	unix.SYS_REBOOT, unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_ACCT, unix.SYS_QUOTACTL, unix.SYS_SYSLOG,
	unix.SYS_SETTIMEOFDAY, unix.SYS_CLOCK_SETTIME, unix.SYS_CLOCK_ADJTIME, unix.SYS_ADJTIMEX,
	unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT, unix.SYS_VHANGUP,
}

const nsFlags = unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWNET | unix.CLONE_NEWPID |
	unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWTIME

func stmt(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }
func jeq(k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: k, Jt: jt, Jf: jf}
}

func buildFilter() ([]unix.SockFilter, error) {
	arch, err := auditArch()
	if err != nil {
		return nil, err
	}
	const (
		ld    = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		ret   = unix.BPF_RET | unix.BPF_K
		allow = unix.SECCOMP_RET_ALLOW
		eperm = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
		enosy = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)
		eafns = unix.SECCOMP_RET_ERRNO | uint32(unix.EAFNOSUPPORT)
		kill  = unix.SECCOMP_RET_KILL_PROCESS
	)
	p := []unix.SockFilter{
		stmt(ld, offArch),
		jeq(arch, 1, 0),
		stmt(ret, kill),
		stmt(ld, offNr),
	}
	for _, nr := range deniedSyscalls {
		p = append(p, jeq(uint32(nr), 0, 1), stmt(ret, eperm))
	}
	// clone3 -> ENOSYS: callers (glibc, Go) fall back to clone, whose flags we can inspect.
	p = append(p, jeq(unix.SYS_CLONE3, 0, 1), stmt(ret, enosy))
	// clone with any namespace flag -> EPERM.
	p = append(p,
		jeq(unix.SYS_CLONE, 0, 4),
		stmt(ld, offArg0),
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: nsFlags, Jt: 0, Jf: 1},
		stmt(ret, eperm),
		stmt(ret, allow),
	)
	// socket(family, …): allow AF_UNIX, AF_INET, AF_INET6 only.
	p = append(p,
		jeq(unix.SYS_SOCKET, 0, 6),
		stmt(ld, offArg0),
		jeq(unix.AF_UNIX, 2, 0),
		jeq(unix.AF_INET, 1, 0),
		jeq(unix.AF_INET6, 0, 1),
		stmt(ret, allow),
		stmt(ret, eafns),
	)
	p = append(p, stmt(ret, allow))
	return p, nil
}

func applySeccomp() error {
	f, err := buildFilter()
	if err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	// TSYNC: apply to every thread of this Go process, not only the calling one.
	_, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if e != 0 {
		return e
	}
	return nil
}
