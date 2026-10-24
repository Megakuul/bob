package evalbox

import (
	"syscall"
	"testing"
	"time"
)

// The filter's own syscall table must agree with every number the standard library knows.
func TestSyscallNumbersMatchStd(t *testing.T) {
	std := map[string][2]uint32{
		"clone":  {sysClone, uint32(syscall.SYS_CLONE)},
		"socket": {sysSocket, uint32(syscall.SYS_SOCKET)},
		"prctl":  {sysPrctl, uint32(syscall.SYS_PRCTL)},
	}
	for name, v := range std {
		if v[0] != v[1] {
			t.Errorf("%s: filter uses %d, syscall package says %d", name, v[0], v[1])
		}
	}
	want := map[uint32]bool{}
	for _, nr := range []uintptr{syscall.SYS_PTRACE, syscall.SYS_MOUNT, syscall.SYS_UMOUNT2, syscall.SYS_PIVOT_ROOT,
		syscall.SYS_CHROOT, syscall.SYS_UNSHARE, syscall.SYS_KEYCTL, syscall.SYS_ADD_KEY,
		syscall.SYS_REQUEST_KEY, syscall.SYS_PERF_EVENT_OPEN, syscall.SYS_KEXEC_LOAD, syscall.SYS_INIT_MODULE,
		syscall.SYS_DELETE_MODULE, syscall.SYS_REBOOT, syscall.SYS_SWAPON, syscall.SYS_SWAPOFF, syscall.SYS_ACCT,
		syscall.SYS_QUOTACTL, syscall.SYS_SYSLOG, syscall.SYS_SETTIMEOFDAY, syscall.SYS_CLOCK_SETTIME,
		syscall.SYS_ADJTIMEX, syscall.SYS_VHANGUP} {
		want[uint32(nr)] = true
	}
	have := map[uint32]bool{}
	for _, nr := range deniedSyscalls {
		have[nr] = true
	}
	for nr := range want {
		if !have[nr] {
			t.Errorf("syscall %d is not denied", nr)
		}
	}
}

// Every jump of the filter must land inside the program, and it must end in a return.
func TestSeccompFilterWellFormed(t *testing.T) {
	p, err := seccompFilter()
	if err != nil {
		t.Fatal(err)
	}
	for i, ins := range p {
		if ins.Code&0x07 == syscall.BPF_JMP {
			if i+1+int(ins.Jt) >= len(p) || i+1+int(ins.Jf) >= len(p) {
				t.Errorf("instruction %d jumps outside the program", i)
			}
		}
	}
	if last := p[len(p)-1]; last.Code != syscall.BPF_RET|syscall.BPF_K || last.K != seccompRetAllow {
		t.Errorf("the filter does not end in RET ALLOW: %+v", last)
	}
}

func TestErrorTexts(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{seconds(5 * time.Second), "5s"}, {seconds(120 * time.Second), "120s"},
		{size(512 << 20), "512M"}, {size(2 << 30), "2G"}, {mib(16 << 20), "16 MiB"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
