#!/bin/sh
# Vulkan detection of a bundled vulkaninfo against host Mesa (lavapipe, CPU) in a Fedora rootfs.
BW=$1; RF=$2; AI=$3
sb() { m=$1; shift; extra=""; [ "$m" = nouserns ] && extra="--disable-userns"
  $BW --unshare-user --unshare-pid --die-with-parent $extra --overlay-src "$RF" --tmp-overlay / --proc /proc --dev /dev --tmpfs /tmp \
   --ro-bind "$AI" /work --clearenv --setenv PATH /usr/bin --setenv HOME /tmp --setenv APPIMAGE_EXTRACT_AND_RUN 1 "$@"; }
for m in userns nouserns; do
 echo "$m default:   $(sb $m /work/vulkaninfo-aarch64.AppImage --summary 2>&1 | grep -E 'deviceName|ERROR|Cannot|error' | head -2 | tr '\n' ' ')"
 echo "$m ICD file:  $(sb $m /bin/sh -c 'VK_ICD_FILENAMES=/usr/share/vulkan/icd.d/lvp_icd.aarch64.json /work/vulkaninfo-aarch64.AppImage --summary' 2>&1 | grep -E 'deviceName|ERROR|rror' | head -2 | tr '\n' ' ')"
 echo "$m ICD+LDLP:  $(sb $m /bin/sh -c 'XDG_DATA_DIRS=/usr/share LD_LIBRARY_PATH=/usr/lib64 /work/vulkaninfo-aarch64.AppImage --summary' 2>&1 | grep -E 'deviceName|ERROR|rror' | head -2 | tr '\n' ' ')"
done
