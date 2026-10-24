package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// The refusal of SPEC R-NIX-022 (ADR 0002 Decision 4). Detection order: the sysctls are read as
// a hint only (on Ubuntu the AppArmor sysctl reads 1 even when a profile exempts bob); the
// decision comes from the clone error class (ENOSPC, EPERM, EACCES) or from the first mount
// failing inside the namespace (child exit 125). There is no fallback mode.

const (
	fixMaxUserNS      = "echo 'user.max_user_namespaces = 15000' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system"
	fixUsernsClone    = "echo 'kernel.unprivileged_userns_clone = 1' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system"
	fixNixOS          = "set security.allowUserNamespaces = true; in configuration.nix and run nixos-rebuild switch"
	fixContainer      = "bob runs only where unprivileged user namespaces are allowed; inside containers, allow them (e.g. podman --security-opt seccomp=unconfined, or docker --security-opt seccomp=unconfined)"
	fixAppArmorSysctl = "echo 'kernel.apparmor_restrict_unprivileged_userns = 0' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system"
)

const (
	causeMaxUserNS   = "user namespaces are disabled (user.max_user_namespaces = 0)"
	causeUsernsClone = "unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone = 0)"
	causeNixOS       = "NixOS has user namespaces turned off (security.allowUserNamespaces = false)"
	causeAppArmor    = "AppArmor restricts unprivileged user namespaces (kernel.apparmor_restrict_unprivileged_userns = 1)"
)

// sysctl reads /proc/sys/<name> (dots → slashes), "" when absent.
func sysctl(name string) string {
	b, err := os.ReadFile(filepath.Join("/proc/sys", strings.ReplaceAll(name, ".", "/")))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func refusal(cause, fix string, raw error) *diag.Error {
	d := diag.New(7005, "fix", fix)
	d.Cause = cause
	if raw != nil {
		d.Raw = []string{raw.Error()}
	}
	return d
}

// cloneRefusal classifies a failed clone of the namespace child. Errors of other classes are
// not a namespace policy and are returned wrapped.
func cloneRefusal(err error) error {
	nixos := false
	if _, serr := os.Stat("/etc/NIXOS"); serr == nil {
		nixos = true
	}
	switch {
	case errors.Is(err, syscall.ENOSPC):
		if nixos {
			return refusal(causeNixOS, fixNixOS, err)
		}
		return refusal(causeMaxUserNS, fixMaxUserNS, err)
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		switch {
		case nixos:
			return refusal(causeNixOS, fixNixOS, err)
		case sysctl("kernel.unprivileged_userns_clone") == "0":
			return refusal(causeUsernsClone, fixUsernsClone, err)
		case sysctl("kernel.apparmor_restrict_unprivileged_userns") == "1":
			return appArmorRefusal(err.Error())
		case sysctl("user.max_user_namespaces") == "0":
			return refusal(causeMaxUserNS, fixMaxUserNS, err)
		}
		name := "EPERM"
		if errors.Is(err, syscall.EACCES) {
			name = "EACCES"
		}
		return refusal("creating a user namespace was denied ("+name+"), usually by a container's seccomp profile or by SELinux", fixContainer, err)
	}
	d := diag.New(9005, "reason", "the namespace child could not be started")
	d.Raw = []string{err.Error()}
	return d
}

// appArmorRefusal is the refusal for a namespace whose first mount was denied: the Ubuntu ≥ 24.04
// AppArmor default. The fix names bob's real path (readlink /proc/self/exe).
func appArmorRefusal(raw string) error {
	exe, err := os.Readlink("/proc/self/exe")
	if err != nil || exe == "" {
		exe, _ = os.Executable()
	}
	if strings.ContainsAny(exe, " \t\"") {
		exe = `"` + strings.ReplaceAll(exe, `"`, `\"`) + `"`
	}
	fix := strings.Join([]string{
		"sudo tee /etc/apparmor.d/bob >/dev/null <<'PROFILE'",
		"abi <abi/4.0>,",
		"include <tunables/global>",
		"profile bob " + exe + " flags=(unconfined) {",
		"  userns,",
		"  include if exists <local/bob>",
		"}",
		"PROFILE",
		"sudo apparmor_parser -r /etc/apparmor.d/bob",
		"alternative (system-wide): " + fixAppArmorSysctl,
	}, "\n")
	d := refusal(causeAppArmor, fix, nil)
	if raw != "" {
		d.Raw = []string{raw}
	}
	return d
}
