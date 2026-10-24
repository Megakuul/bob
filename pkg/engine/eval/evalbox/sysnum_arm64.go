package evalbox

// Syscall numbers of linux/arm64 (asm-generic table). The standard library's syscall tables
// stop before bpf/io_uring/the new mount API, so the filter carries its own; sysnum_test.go
// checks every number the standard library also defines.

const auditArch = 0xC00000B7 // AUDIT_ARCH_AARCH64

const (
	sysClone   = 220
	sysSocket  = 198
	sysPrctl   = 167
	sysSeccomp = 277
)

var deniedSyscalls = []uint32{
	117,      // ptrace
	270, 271, // process_vm_readv, process_vm_writev
	40, 39, 41, 51, // mount, umount2, pivot_root, chroot
	97, 268, // unshare, setns
	219, 217, 218, // keyctl, add_key, request_key
	280, 241, 282, // bpf, perf_event_open, userfaultfd
	104, 294, 105, 273, 106, // kexec_load, kexec_file_load, init_module, finit_module, delete_module
	142, 224, 225, 89, 60, 116, // reboot, swapon, swapoff, acct, quotactl, syslog
	170, 112, 266, 171, // settimeofday, clock_settime, clock_adjtime, adjtimex
	265, 264, 58, // open_by_handle_at, name_to_handle_at, vhangup
}
