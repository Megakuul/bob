package evalbox

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Bind sources. stage1 runs in its own mount namespace, so it cannot bind-mount a descriptor
// of the host namespace, and as an unprivileged uid it may not reach host paths below a 0700
// directory (root's home). When bob runs as root it therefore clones each source with
// open_tree(OPEN_TREE_CLONE), makes the detached copy read-only, and hands the descriptor to
// stage1, which attaches it with move_mount. An unprivileged bob passes plain paths: it can
// reach them itself, and stage1 is that same kernel uid.

const (
	atFdcwd             = -100
	atEmptyPath         = 0x1000
	atRecursive         = 0x8000
	openTreeClone       = 1
	moveMountFEmptyPath = 4
	mountAttrRdonly     = 0x1
	mountAttrNosuid     = 0x2
	mountAttrNodev      = 0x4
)

type mountAttr struct {
	set, clr, propagation, userns uint64
}

// bindSource prepares the source of one bind for stage1; detached trees are appended to files
// (inherited from fd 3 on, in order).
func bindSource(host, target string, rw bool, files *[]*os.File) (stageBind, error) {
	if !hostRoot() {
		if _, err := os.Stat(host); err != nil {
			return stageBind{}, fmt.Errorf("evaluation sandbox: %w", err)
		}
		return stageBind{FD: -1, Path: host, Target: target, RW: rw}, nil
	}
	p, err := syscall.BytePtrFromString(host)
	if err != nil {
		return stageBind{}, err
	}
	fd, _, e := syscall.Syscall6(sysOpenTree, uintptr(atFdcwdU()), uintptr(unsafe.Pointer(p)),
		openTreeClone|syscall.O_CLOEXEC|atRecursive, 0, 0, 0)
	if e != 0 {
		return stageBind{}, fmt.Errorf("evaluation sandbox: clone mount of %s: %w", host, e)
	}
	f := os.NewFile(fd, host)
	*files = append(*files, f)
	attr := mountAttr{set: mountAttrNosuid | mountAttrNodev}
	if !rw {
		attr.set |= mountAttrRdonly
	}
	empty, _ := syscall.BytePtrFromString("")
	_, _, e = syscall.Syscall6(sysMountSetattr, fd, uintptr(unsafe.Pointer(empty)), atEmptyPath|atRecursive,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return stageBind{}, fmt.Errorf("evaluation sandbox: read-only mount of %s: %w", host, e)
	}
	return stageBind{FD: graphFD + len(*files) - 1, Target: target, RW: rw}, nil
}

// atFdcwdU is AT_FDCWD as the unsigned register value.
func atFdcwdU() uint64 {
	v := int64(atFdcwd)
	return uint64(v)
}

// attach mounts b at dst (stage1). Read-only is load-bearing: in a rootless store the
// evaluator's kernel uid owns the toolchain files.
func (b stageBind) attach(dst string) error {
	if b.FD < 0 {
		if b.RW {
			if err := mountpoint(dst, true); err != nil {
				return err
			}
			return wrap(syscall.Mount(b.Path, dst, "", syscall.MS_BIND|syscall.MS_REC, ""), "bind "+dst)
		}
		return bindRO(b.Path, dst)
	}
	if err := mountpoint(dst, true); err != nil {
		return err
	}
	empty, _ := syscall.BytePtrFromString("")
	t, err := syscall.BytePtrFromString(dst)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall6(sysMoveMount, uintptr(b.FD), uintptr(unsafe.Pointer(empty)), uintptr(atFdcwdU()),
		uintptr(unsafe.Pointer(t)), moveMountFEmptyPath, 0)
	if e != 0 {
		return fmt.Errorf("attach %s: %w", dst, e)
	}
	_ = syscall.Close(b.FD)
	return nil
}
