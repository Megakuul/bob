package harness

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

// The host's /dev/null was twice replaced by a regular root-owned file while the suites ran as
// root (I-X1): zsh's `compinit -d /dev/null` writes its dump next to the dump file and `mv -f`s
// it over /dev/null. Every later `>/dev/null` on the host then appends to that file. Main makes
// a test binary fail loudly, naming itself, when /dev/null is not the null device after its
// tests, so the next clobbering is caught by the suite that did it.

// devNullRdev is makedev(1, 3), the null device.
const devNullRdev = 1<<8 | 3

// checkNull reports why path is not the null character device (1,3); nil when it is.
func checkNull(path string) error {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return fmt.Errorf("%s is not a character device (mode %#o, owner uid %d, %d bytes)", path, st.Mode, st.Uid, st.Size)
	}
	if st.Rdev != devNullRdev {
		return fmt.Errorf("%s is the character device %d,%d, not 1,3", path, st.Rdev>>8&0xfff, st.Rdev&0xff)
	}
	return nil
}

// CheckDevNull reports whether the host's /dev/null is still the null device.
func CheckDevNull() error { return checkNull(os.DevNull) }

// Main runs the tests of m and exits with their status, failing the binary when /dev/null is
// not the null device before or after them. Use it as `func TestMain(m *testing.M) {
// harness.Main(m) }` in suites that run shells or other host tools as root.
func Main(m *testing.M) {
	if err := CheckDevNull(); err != nil {
		fmt.Fprintf(os.Stderr, "\nHARNESS ERROR: host damage before %s ran: %v\n"+
			"restore it as root: rm -f /dev/null && mknod -m 666 /dev/null c 1 3\n\n", os.Args[0], err)
		os.Exit(3)
	}
	code := m.Run()
	if err := CheckDevNull(); err != nil {
		fmt.Fprintf(os.Stderr, "\nHARNESS ERROR: HOST DAMAGE: after the tests of %s, %v\n"+
			"a test (or a tool it ran as root) replaced or deleted /dev/null; find it before rerunning.\n"+
			"restore it as root: rm -f /dev/null && mknod -m 666 /dev/null c 1 3\n\n", os.Args[0], err)
		if code == 0 {
			code = 3
		}
	}
	os.Exit(code)
}
