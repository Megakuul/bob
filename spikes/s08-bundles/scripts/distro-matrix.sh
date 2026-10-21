#!/bin/sh
# Run bob AppImages inside plain distro rootfs (no Nix) in an unprivileged bwrap sandbox.
# Usage (as an unprivileged user): distro-matrix.sh BWRAP ROOTFS_DIR APPIMAGE_DIR
# Modes: ext+userns (runtime extract-and-run, bobrun userns), ext+nouserns (bwrap --disable-userns,
# bobrun falls back to ld.so), fuse (only /dev/fuse bound; no setuid fusermount in a sandbox).
BW=$1; RF=$2; AI=$3
sb() { # distro mode cmd...
  d=$1; m=$2; shift 2
  extra=""; [ "$m" = nouserns ] && extra="--disable-userns"
  envs="--setenv APPIMAGE_EXTRACT_AND_RUN 1"; [ "$m" = fuse ] && envs="--dev-bind /dev/fuse /dev/fuse"
  $BW --unshare-user --unshare-pid --unshare-ipc --unshare-uts --share-net --die-with-parent $extra \
    --overlay-src "$RF/$d" --tmp-overlay / --proc /proc --dev /dev --tmpfs /tmp \
    --ro-bind /etc/resolv.conf /etc/resolv.conf --ro-bind "$AI" /work \
    --clearenv --setenv PATH /usr/local/bin:/usr/bin:/bin --setenv HOME /tmp --setenv TMPDIR /tmp $envs \
    "$@"
}
for d in ubuntu-22.04 ubuntu-24.04 debian-12 fedora-43 alpine-3.22; do
  for m in userns nouserns; do
    api=$(sb $d $m /work/api-aarch64.AppImage 2>&1 | tail -1 | cut -c1-30)
    https=$(sb $d $m /work/curl-aarch64.AppImage -sS -o /dev/null -w '%{http_code}' https://cache.nixos.org/nix-cache-info 2>&1 | tail -1 | cut -c1-60)
    loc=$(sb $d $m /bin/sh -c 'LANG=en_US.UTF-8 BOB_ENTRY=locale /work/curl-aarch64.AppImage 2>&1 | grep -c "Cannot set"; ' 2>&1 | tail -1)
    date=$(sb $d $m /bin/sh -c 'LANG=en_US.UTF-8 TZ=UTC BOB_ENTRY=date /work/curl-aarch64.AppImage -d @0 "+%c"' 2>&1 | tail -1)
    iconv=$(sb $d $m /bin/sh -c 'printf "\303\244" | BOB_ENTRY=iconv /work/curl-aarch64.AppImage -f UTF-8 -t ISO-8859-15 | od -An -tx1' 2>&1 | tail -1 | tr -d ' ')
    exe=$(sb $d $m /bin/sh -c 'BOB_ENTRY=ls /work/curl-aarch64.AppImage -l /proc/self/exe' 2>&1 | tail -1 | sed 's/.*-> //' | sed 's#.*/##')
    echo "$d | $m | api: $api | https: $https | locale-warnings: $loc | date: $date | iconv: $iconv | /proc/self/exe: $exe"
  done
done
