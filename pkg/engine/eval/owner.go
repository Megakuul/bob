package eval

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ownerFile is bob's ownership stamp of .bob/eval: the device, inode and change time of the
// lock file that this evaluator created there.
const ownerFile = "owner"

// claimGenDir takes the exclusive evaluator lock of gen (.bob/eval) and makes sure that what
// gen holds was written by bob on this machine. A .bob/ that came with the repository (a
// committed one in a worker's checkout, an archive, a copy) is not bob's state: its evaluator
// binary and key would skip the rule checks and the compile, its index would feed the hashes,
// and its GOCACHE upper layer belongs to another user, so the sandbox cannot rewrite its
// entries (I-B26: "open …/gocache/…-a: permission denied"). Such a directory is emptied and
// stamped anew; the evaluation then starts cold.
//
// The stamp names the lock file's change time, which the kernel sets when bob creates the file
// and nobody can set to a chosen value, so a committed stamp never matches (decision A201).
func claimGenDir(gen string) (*os.File, error) {
	p := filepath.Join(gen, lockFile)
	lf, err := lock(p)
	if err != nil {
		// a lock that is not a regular file (a committed symlink or directory) is not bob's
		if fi, lerr := os.Lstat(p); lerr != nil || fi.Mode().IsRegular() {
			return nil, err
		}
		if rerr := os.RemoveAll(p); rerr != nil {
			return nil, fmt.Errorf("%w (removing it: %v)", err, rerr)
		}
		if lf, err = lock(p); err != nil {
			return nil, err
		}
	}
	stamp, err := lockStamp(lf)
	if err != nil {
		_ = lf.Close()
		return nil, err
	}
	if b, err := readCapped(filepath.Join(gen, ownerFile), ownerFile, 256); err == nil && string(b) == stamp {
		return lf, nil
	}
	if err := emptyExcept(gen, lockFile); err != nil {
		_ = lf.Close()
		return nil, fmt.Errorf("removing evaluator state bob did not write (%s): %w", gen, err)
	}
	if err := os.WriteFile(filepath.Join(gen, ownerFile), []byte(stamp), 0o644); err != nil {
		_ = lf.Close()
		return nil, err
	}
	return lf, nil
}

// lockStamp is the stamp of the open lock file f.
func lockStamp(f *os.File) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return "", fmt.Errorf("stat %s: %w", f.Name(), err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return "", fmt.Errorf("%s is not a regular file", f.Name())
	}
	return fmt.Sprintf("v1 %d %d %d.%09d\n", st.Dev, st.Ino, st.Ctim.Sec, st.Ctim.Nsec), nil
}

// emptyExcept removes every entry of dir but keep. Entries are removed without following
// symlinks (os.RemoveAll), so a committed link cannot point the removal elsewhere.
func emptyExcept(dir, keep string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		if e.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
