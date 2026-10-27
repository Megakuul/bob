package store

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The user's own identity in dev sessions (A561). bob's namespace maps the host user to uid 0
// (the build sandbox and Nix need a root they own), so a program started in it shows `root` in
// `id`, `whoami` and its prompt. Programs of the user's own session (the dev shell, `bob dev --
// cmd`, dev hooks and servers, `bob exec`: the shims of the dev tools) run as the user instead:
// they are started in a nested user namespace in which the user's id maps to the id the user
// has in the outer namespace, so files they create belong to the user, /etc/passwd names the
// user, and git and ssh see their own files. The mounts of the namespace are kept (a user
// namespace does not change the mount namespace). Builds and Nix keep the root mapping.

const (
	capSysAdmin          = 21 // CAP_SYS_ADMIN, to mount the host store view before the program starts
	prCapAmbient         = 47 // prctl(PR_CAP_AMBIENT, …)
	prCapAmbientClearAll = 4  // PR_CAP_AMBIENT_CLEAR_ALL
)

// realIDs is the user's uid and gid in the namespace that maps this process (the host's, or the
// outer namespace's for a bob started in a dev session) when this process runs as the root of a
// namespace of its own; ok is false in every other case: the process then already is the user.
func realIDs() (uid, gid int, ok bool) {
	if os.Getuid() != 0 || os.Getgid() != 0 {
		return 0, 0, false
	}
	uid, ok1 := outerID("/proc/self/uid_map")
	gid, ok2 := outerID("/proc/self/gid_map")
	if !ok1 || !ok2 || uid == 0 {
		return 0, 0, false
	}
	return uid, gid, true
}

// outerID is the id that the map file maps to id 0 (the second field of the line whose first
// field is 0).
func outerID(file string) (int, bool) {
	f, err := os.Open(file)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 3 || fs[0] != "0" {
			continue
		}
		n, err := strconv.Atoi(fs[1])
		return n, err == nil
	}
	return 0, false
}

// asUser makes cmd start in a nested user namespace in which uid and gid are the ids of the
// user and map to the root of the current one. With mount true the program gets CAP_SYS_ADMIN
// as an ambient capability, for the child that mounts before it drops it and executes the
// program (hostViewChild).
func asUser(cmd *exec.Cmd, uid, gid int, mount bool) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.Cloneflags |= syscall.CLONE_NEWUSER
	attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: 0, Size: 1}}
	attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: 0, Size: 1}}
	attr.GidMappingsEnableSetgroups = false
	if mount {
		attr.AmbientCaps = append(attr.AmbientCaps, capSysAdmin)
	}
	cmd.SysProcAttr = attr
}

// dropAmbient removes the ambient capabilities, so the program that is executed next has none
// (it runs as the user).
func dropAmbient() {
	_, _, _ = syscall.Syscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0, 0, 0, 0)
}
