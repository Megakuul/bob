package evalbox

import (
	"errors"
	"syscall"
	"unsafe"
)

// The seccomp layer (S13 §5.2): a classic-BPF deny-list built without libseccomp. It removes
// kernel attack surface a build definition never needs: namespaces, mounts, ptrace, bpf,
// io_uring, keyrings, perf, userfaultfd, modules/kexec, clock changes, and every socket family
// except unix/inet/inet6 (AF_VSOCK is not network-namespaced).

const (
	seccompRetAllow       = 0x7fff0000
	seccompRetErrno       = 0x00050000
	seccompRetKillProcess = 0x80000000
	seccompSetModeFilter  = 1
	seccompFlagTsync      = 1

	// Offsets into struct seccomp_data.
	offNr   = 0
	offArch = 4
	offArg0 = 16 // low 32 bits of args[0] (little-endian)

	x32Bit = 0x40000000 // amd64 x32 ABI syscalls carry this bit

	// Syscalls >= 424 have the same number on every architecture.
	sysIOURingSetup    = 425
	sysIOURingEnter    = 426
	sysIOURingRegister = 427
	sysOpenTree        = 428
	sysMoveMount       = 429
	sysFsopen          = 430
	sysFsconfig        = 431
	sysFsmount         = 432
	sysFspick          = 433
	sysClone3          = 435
	sysPidfdGetfd      = 438
	sysMountSetattr    = 442
	sysOpenTreeAttr    = 467

	afUnix  = 1
	afInet  = 2
	afInet6 = 10

	// Every CLONE_NEW* flag, incl. CLONE_NEWTIME (0x80) and CLONE_NEWCGROUP.
	cloneNSFlags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID |
		syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | 0x02000000 | 0x80
)

func stmt(code uint16, k uint32) syscall.SockFilter { return syscall.SockFilter{Code: code, K: k} }

func jeq(k uint32, jt, jf uint8) syscall.SockFilter {
	return syscall.SockFilter{Code: syscall.BPF_JMP | syscall.BPF_JEQ | syscall.BPF_K, K: k, Jt: jt, Jf: jf}
}

// seccompFilter returns the BPF program of the evaluator.
func seccompFilter() ([]syscall.SockFilter, error) {
	if auditArch == 0 {
		return nil, errors.New("no syscall filter for this architecture")
	}
	const (
		ld    = syscall.BPF_LD | syscall.BPF_W | syscall.BPF_ABS
		ret   = syscall.BPF_RET | syscall.BPF_K
		eperm = seccompRetErrno | uint32(syscall.EPERM)
		enosy = seccompRetErrno | uint32(syscall.ENOSYS)
		eafns = seccompRetErrno | uint32(syscall.EAFNOSUPPORT)
	)
	p := []syscall.SockFilter{
		stmt(ld, offArch),
		jeq(auditArch, 1, 0),
		stmt(ret, seccompRetKillProcess), // wrong arch
		stmt(ld, offNr),
		// x32 syscalls on amd64 would bypass the numbers below.
		{Code: syscall.BPF_JMP | syscall.BPF_JSET | syscall.BPF_K, K: x32Bit, Jt: 0, Jf: 1},
		stmt(ret, seccompRetKillProcess),
	}
	denied := append([]uint32{
		sysIOURingSetup, sysIOURingEnter, sysIOURingRegister, sysOpenTree, sysMoveMount, sysFsopen,
		sysFsconfig, sysFsmount, sysFspick, sysPidfdGetfd, sysMountSetattr, sysOpenTreeAttr,
	}, deniedSyscalls...)
	for _, nr := range denied {
		p = append(p, jeq(nr, 0, 1), stmt(ret, eperm))
	}
	// clone3 -> ENOSYS: callers (Go, glibc) fall back to clone, whose flags BPF can inspect.
	p = append(p, jeq(sysClone3, 0, 1), stmt(ret, enosy))
	// clone with any namespace flag -> EPERM.
	p = append(p,
		jeq(sysClone, 0, 4),
		stmt(ld, offArg0),
		syscall.SockFilter{Code: syscall.BPF_JMP | syscall.BPF_JSET | syscall.BPF_K, K: cloneNSFlags, Jt: 0, Jf: 1},
		stmt(ret, eperm),
		stmt(ret, seccompRetAllow),
	)
	// socket(family, ...): AF_UNIX, AF_INET and AF_INET6 only.
	p = append(p,
		jeq(sysSocket, 0, 6),
		stmt(ld, offArg0),
		jeq(afUnix, 2, 0),
		jeq(afInet, 1, 0),
		jeq(afInet6, 0, 1),
		stmt(ret, seccompRetAllow),
		stmt(ret, eafns),
	)
	return append(p, stmt(ret, seccompRetAllow)), nil
}

// applySeccomp installs the filter on every thread of this process (TSYNC). The caller has set
// no_new_privs.
func applySeccomp() error {
	f, err := seccompFilter()
	if err != nil {
		return err
	}
	prog := syscall.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, e := syscall.RawSyscall(sysSeccomp, seccompSetModeFilter, seccompFlagTsync, uintptr(unsafe.Pointer(&prog)))
	if e != 0 {
		return e
	}
	return nil
}
