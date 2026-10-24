package evalbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The Landlock layer (S13 §5.2): a second filesystem allow-list behind the mount namespace,
// "no TCP bind or connect at all", and scoping of abstract unix sockets and signals to the
// sandbox. Raw syscalls; the best ABI the kernel offers is used.

const (
	sysLandlockCreateRuleset = 444 // same number on every architecture
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1
	oPath                        = 0x200000 // O_PATH (same value on arm64 and amd64; missing from syscall on amd64)
	landlockRulePathBeneath      = 1

	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llReadFile   = 1 << 2
	llReadDir    = 1 << 3
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13 // ABI 2
	llTruncate   = 1 << 14 // ABI 3
	llIoctlDev   = 1 << 15 // ABI 5

	llNetBindTCP    = 1 << 0 // ABI 4
	llNetConnectTCP = 1 << 1

	llScopeAbstractUnix = 1 << 0 // ABI 6
	llScopeSignal       = 1 << 1

	llRead  = llReadFile | llReadDir
	llWrite = llWriteFile | llRemoveDir | llRemoveFile | llMakeChar | llMakeDir | llMakeReg |
		llMakeSock | llMakeFifo | llMakeBlock | llMakeSym
	llFileOnly = llExecute | llWriteFile | llReadFile | llTruncate | llIoctlDev
)

type landlockRulesetAttr struct {
	handledFS  uint64
	handledNet uint64
	scoped     uint64
}

// landlockPathBeneath is struct landlock_path_beneath_attr (packed, 12 bytes; the kernel reads
// only those).
type landlockPathBeneath struct {
	allowed  uint64
	parentFD int32
}

// LandlockABI returns the Landlock ABI version of the running kernel (0 = unavailable).
func LandlockABI() int {
	v, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if e != 0 {
		return 0
	}
	return int(v)
}

type landlockRule struct {
	path   string
	access uint64
}

// applyLandlock restricts the calling thread (and everything it executes) to rules.
func applyLandlock(rules []landlockRule) error {
	abi := LandlockABI()
	if abi < 1 {
		return errors.New("landlock is not available")
	}
	handled := uint64(llExecute | llRead | llWrite)
	if abi >= 2 {
		handled |= llRefer
	}
	if abi >= 3 {
		handled |= llTruncate
	}
	if abi >= 5 {
		handled |= llIoctlDev
	}
	attr := landlockRulesetAttr{handledFS: handled}
	size := unsafe.Sizeof(uint64(0))
	if abi >= 4 {
		attr.handledNet = llNetBindTCP | llNetConnectTCP // handled with no rule: all denied
		size = 2 * unsafe.Sizeof(uint64(0))
	}
	if abi >= 6 {
		attr.scoped = llScopeAbstractUnix | llScopeSignal
		size = 3 * unsafe.Sizeof(uint64(0))
	}
	fd, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), size, 0)
	if e != 0 {
		return fmt.Errorf("create ruleset (abi %d): %w", abi, e)
	}
	defer syscall.Close(int(fd))
	for _, r := range rules {
		pfd, err := syscall.Open(r.path, oPath|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue // not present in this sandbox
		}
		acc := r.access & handled
		if fi, err := os.Stat(r.path); err == nil && !fi.IsDir() {
			acc &= llFileOnly
		}
		pb := landlockPathBeneath{allowed: acc, parentFD: int32(pfd)}
		_, _, e := syscall.RawSyscall6(sysLandlockAddRule, fd, landlockRulePathBeneath, uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
		_ = syscall.Close(pfd)
		if e != 0 {
			return fmt.Errorf("add rule %s: %w", r.path, e)
		}
	}
	if _, _, e := syscall.RawSyscall(sysLandlockRestrictSelf, fd, 0, 0); e != 0 {
		return fmt.Errorf("restrict self: %w", e)
	}
	return nil
}
