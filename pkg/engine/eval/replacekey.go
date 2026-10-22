package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// replacePrefix starts the eval-key names of files of locally replaced dependency bob
// repositories. Repository-relative paths of the scan never start with "../".
const replacePrefix = "../replace/"

// addReplaceKeys adds the go.mod and every bob.go of each directory replace target of the root
// go.mod to index (R-FW-043.1, amendment A90): editing a dependency bob repository checked out
// next to the repository changes the eval key. Like the scan's own files they are re-read only
// when size or mtime differ from prev. A target that does not exist, or that an untrusted
// evaluation would refuse, adds nothing (the generation reports it when it is used).
func addReplaceKeys(root string, prev, index map[string]indexEntry, untrusted bool) error {
	gm, err := readCapped(filepath.Join(root, "go.mod"), "go.mod", maxGoModBytes)
	if err != nil {
		return err
	}
	mod := parseGoMod(gm)
	mps := make([]string, 0, len(mod.replace))
	for mp := range mod.replace {
		mps = append(mps, mp)
	}
	sort.Strings(mps)
	for _, mp := range mps {
		if checkModulePath(mp, false) != nil {
			continue // refused by the generation when used
		}
		src, err := replaceDir(root, mp, mod.replace[mp], untrusted)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
			continue
		}
		dirs, err := walkBobRepo(src, mp)
		if err != nil {
			return err
		}
		files := []string{"go.mod"}
		for _, d := range dirs {
			files = append(files, bobPath(d))
		}
		for _, rel := range files {
			name := replacePrefix + mp + "/" + rel
			e, err := hashFile(filepath.Join(src, filepath.FromSlash(rel)), mp+"/"+rel, prev[name])
			if errors.Is(err, fs.ErrNotExist) && rel == "go.mod" {
				continue
			}
			if err != nil {
				return err
			}
			index[name] = e
		}
	}
	return nil
}

// hashFile returns the index entry of the file p, reusing old when size and mtime match.
func hashFile(p, shown string, old indexEntry) (indexEntry, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return indexEntry{}, err
	}
	e := indexEntry{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
	if old.SHA256 != "" && old.Size == e.Size && old.MTime == e.MTime {
		return old, nil
	}
	b, err := readCapped(p, shown, capOf(path.Base(p)))
	if err != nil {
		return indexEntry{}, err
	}
	sum := sha256.Sum256(b)
	e.SHA256 = hex.EncodeToString(sum[:])
	return e, nil
}
