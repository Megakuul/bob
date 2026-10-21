with import ./common.nix;
let
  probes = ''
    exec 2>&1
    p() { echo "## $*"; }
    p id; id
    p hostname; hostname
    p status; grep -E 'Cap(Eff|Bnd)|NoNewPrivs|Seccomp' /proc/self/status
    p pids-visible; ls /proc | grep -E '^[0-9]+$' | tr '\n' ' '; echo
    p root; ls / | tr '\n' ' '; echo
    p dev; ls /dev | tr '\n' ' '; echo
    p dev-shm; df -h /dev/shm | tail -1
    p etc; ls /etc | tr '\n' ' '; echo; cat /etc/hosts
    p ifaces; ip -o addr | awk '{print $2, $4}'
    p store-entries; ls /nix/store | wc -l
    p home; ls /home 2>&1 | head -2
    p sys; ls /sys 2>&1 | head -3 | tr '\n' ' '; echo
    p kmsg; head -c 80 /dev/kmsg 2>&1 | head -1; dmesg 2>&1 | head -1
    p net-internet; wget -q -T 3 -O- http://1.1.1.1 >/dev/null 2>&1 && echo OPEN || echo blocked
    p net-host-loopback-21300; (echo x | nc -w 2 127.0.0.1 21300) >/dev/null 2>&1 && echo OPEN || echo blocked
    p host-abstract-socket-visible; grep -c s13-bait /proc/net/unix || true
    p store-write; chmod u+w ${bb}/bin/busybox && echo CHMOD-INPUT-OK || echo chmod-input-denied
    p chroot-store-dir; touch /nix/store/s13-injected 2>&1 && echo chroot-store-dir-writable || echo chroot-store-dir-ro
    p setuid; touch $TMPDIR/f; chmod u+s $TMPDIR/f 2>&1 && echo SETUID-OK || echo setuid-denied
    p proc-sys-write; echo x > /proc/sys/kernel/hostname 2>&1 && echo WRITABLE || echo ro
    p userns-nested; unshare -U true 2>&1 && echo NESTED-USERNS-OK || echo nested-userns-denied
    p mount-tmpfs; mkdir -p $TMPDIR/m; mount -t tmpfs t $TMPDIR/m 2>&1 && echo MOUNT-OK || echo mount-denied
    p cgroup; cat /proc/self/cgroup; ls /sys/fs/cgroup 2>&1 | head -2
    p leave-background-process; (exec -a s13-leftover sleep 300) & echo started
  '';
in {
  normal = sh "s13-probe" (probes + "echo done > $out");
  # Fixed-output derivation: same probes, network allowed by Nix. Output is fixed ("ok").
  fod = derivation {
    name = "s13-fod-probe-${stamp}";
    system = "aarch64-linux";
    builder = "${bash}/bin/bash";
    args = [ "-c" (probes + "echo ok > $out") ];
    PATH = "${cu}/bin:${bb}/bin";
    outputHashMode = "flat";
    outputHashAlgo = "sha256";
    outputHash = "sha256-3FG4yWwtdF3zvVWQ2ZAjCkgv0kcSNZlUjgYy/b+X/CI=";
  };
}
