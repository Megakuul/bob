with import ./common.nix;
let victim = builtins.toFile "s13-victim-fixed" "original\n"; in
sh "s13-storewrite" ''
  exec 2>&1
  ls -l ${victim}; stat -c '%u %a' ${victim}
  chmod u+w ${victim} && echo CHMOD-OK || echo chmod-denied
  stat -c 'after chmod: %u %a' ${victim}
  echo poisoned >> ${victim} && echo WRITE-OK || echo write-denied
  cat ${victim}
  grep ' /nix/store' /proc/self/mountinfo | head -5
  echo ${victim} > $out
''
