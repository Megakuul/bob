package evalbox

// Syscall numbers of linux/amd64 (x86_64 table). See sysnum_arm64.go.

const auditArch = 0xC000003E // AUDIT_ARCH_X86_64

const (
	sysClone   = 56
	sysSocket  = 41
	sysPrctl   = 157
	sysSeccomp = 317
)

var deniedSyscalls = []uint32{
	101,      // ptrace
	310, 311, // process_vm_readv, process_vm_writev
	165, 166, 155, 161, // mount, umount2, pivot_root, chroot
	272, 308, // unshare, setns
	250, 248, 249, // keyctl, add_key, request_key
	321, 298, 323, // bpf, perf_event_open, userfaultfd
	246, 320, 175, 313, 176, // kexec_load, kexec_file_load, init_module, finit_module, delete_module
	169, 167, 168, 163, 179, 103, // reboot, swapon, swapoff, acct, quotactl, syslog
	164, 227, 305, 159, // settimeofday, clock_settime, clock_adjtime, adjtimex
	304, 303, 153, // open_by_handle_at, name_to_handle_at, vhangup
	134, 172, 173, // uselib, iopl, ioperm
}
