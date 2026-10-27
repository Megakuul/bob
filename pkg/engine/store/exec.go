package store

import (
	"os/exec"
)

// StartIn starts cmd inside bob's namespace for the state home home (SPEC §02.7.3: bob exec and
// the shims). cmd.Path must be absolute; cmd.Args, Env, Dir and the standard streams are the
// caller's and reach the program unchanged (unlike Store.Command, no Nix environment is added).
// When this process already runs in bob's namespace, cmd starts as it is. Otherwise the
// namespace child sets the namespace up and executes the program in place, so no second bob
// stays behind it: the caller's cmd.Process is the program, and cmd.Wait reports its status.
// It needs no Store: a shim costs one process start plus the namespace setup.
func StartIn(home string, cmd *exec.Cmd) error {
	home, err := HomeDir(home)
	if err != nil {
		return err
	}
	if _, ok := Entered(); ok || inNamespace(home) {
		if uid, gid, real := realIDs(); real && len(cmd.ExtraFiles) == 0 {
			asUser(cmd, uid, gid, false) // a program of the user's session runs as the user
		}
		return cmd.Start()
	}
	path, args := cmd.Path, cmd.Args
	r, w, err := prepareNamespace(cmd, home, append([]string{modeExec, path}, args...)...)
	if err != nil {
		return err
	}
	return startNamespace(cmd, r, w)
}

// Inside reports whether this process runs inside bob's namespace for the state home home.
func Inside(home string) bool {
	if _, ok := Entered(); ok {
		return true
	}
	h, err := HomeDir(home)
	return err == nil && inNamespace(h)
}
