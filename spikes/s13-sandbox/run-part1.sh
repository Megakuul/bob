#!/bin/bash
# S13 part 1: hostile bob.go evaluations under evalbox, as the unprivileged user bobtest, inside a
# systemd transient service with Delegate=yes (models a worker unit: User=bob-worker, Delegate=yes).
# Run as root (only to prepare files and start the unit). Output: tmp/part1.log
set -u
S=/root/bob/spikes/s13-sandbox
H=/home/bobtest/s13
GO=$(dirname "$(dirname "$(readlink -f "$(command -v go)")")")   # …/share/go (GOROOT)
GOSTORE=$(echo "$GO" | cut -d/ -f1-4)
BASH_STORE=$(grep -- '-bash-' $S/tmp/go-closure.txt | head -1)

rm -rf $H && mkdir -p $H/{bin,src,seed,mod} /home/bobtest/.config/bob-worker
cp $S/bin/evalbox $S/bin/bait $H/bin/
cp -r $S/testdata/evil/. $H/src/
cp $S/tmp/go-closure.txt $H/store.list
echo "AWS_SECRET_ACCESS_KEY=fake-worker-secret" > /home/bobtest/.config/bob-worker/credentials
chmod 600 /home/bobtest/.config/bob-worker/credentials
chown -R bobtest:bobtest $H /home/bobtest/.config

cat > $H/run.sh <<EOF
#!/bin/bash
# runs as bobtest inside the delegated unit
export AWS_SECRET_ACCESS_KEY=fake-worker-secret BOB_WORKER_TOKEN=fake
cd $H
./bin/bait $H/../s13-bait.sock & BAIT=\$!
sleep 0.3
EB="./bin/evalbox -src $H/src -store @$H/store.list -modcache $H/mod \
  -env PATH=$GO/bin:$BASH_STORE/bin -env GOROOT=$GO -env GOCACHE=/work/gocache -env GOMODCACHE=/gomod \
  -env GOPROXY=off -env GOTOOLCHAIN=local -env CGO_ENABLED=0 -env GOFLAGS=-mod=mod -env HOME=/work/home \
  -env TMPDIR=/work/tmp -env BOB_BASH=$BASH_STORE/bin/bash"
export EVALBOX_STATS=1
run() { # name, extra flags, case
  echo "=== \$1"
  s=\$(date +%s%N)
  \$EB \$2 -- bash -c "go build -o /work/e . && exec /work/e \$3" 2>&1 | tail -\${TAILN:-40}
  echo "rc=\${PIPESTATUS[0]} wall=\$(( (\$(date +%s%N)-s)/1000000 )) ms"
}
# Seed cache: a trusted warm GOCACHE built once by the worker (here: build of the stdlib packages
# the evaluator uses, inside the same sandbox, copied out). Measured separately: cold vs seeded.
run "cold build, case ok (no seed cache)" "" ok
# the sandbox as bobtest on a trusted warm-up program (stdlib only), which is what a worker would do.
mkdir -p $H/warm && cd $H/warm && printf 'module warm\ngo 1.26\n' > go.mod && printf 'package main\nimport (_ "fmt"; _ "net"; _ "os/exec"; _ "encoding/json"; _ "syscall"; _ "bytes"; _ "path/filepath")\nfunc main(){}\n' > main.go
PATH=$GO/bin:/usr/bin GOROOT=$GO GOCACHE=$H/seed GOTOOLCHAIN=local CGO_ENABLED=0 HOME=$H go build -o /dev/null . ; cd $H
echo "seed cache size: \$(du -sh $H/seed | cut -f1)"
EB="\$EB -seedcache $H/seed"
run "seeded build, case ok" "-timing" ok
run "seeded build, case ok (2nd)" "" ok
run "secrets" "" secrets
run "network" "" network
run "escape" "" escape
TAILN=6 run "forkbomb (Go self-exec)" "-pids 128 -timeout 30s" forkbomb-go
TAILN=6 run "forkbomb (bash)" "-pids 128 -timeout 30s" forkbomb-sh
TAILN=6 run "thread bomb" "-pids 128 -timeout 30s" threadbomb
TAILN=6 run "memory bomb" "-mem 512M -timeout 60s" membomb
TAILN=6 run "cpu loop" "-timeout 5s -cpus 1" cpuloop
TAILN=6 run "disk fill" "-work 256m -mem 1G" diskfill
TAILN=6 run "big graph output" "-outmax 16777216" bigoutput
echo "=== no landlock / no seccomp (layers removed one at a time, escape+network)"
TAILN=40 run "escape without seccomp" "-no-seccomp" escape
TAILN=40 run "network without landlock" "-no-landlock" network
TAILN=40 run "secrets without landlock" "-no-landlock" secrets
echo "=== host side after all cases"
echo "leftover eval leaves: \$(ls -d /sys/fs/cgroup\$(cut -d: -f3 /proc/self/cgroup)/eval-* 2>/dev/null | wc -l)"; echo "cgroup tree:"; find /sys/fs/cgroup\$(cut -d: -f3 /proc/self/cgroup)/ -maxdepth 1 -type d
kill \$BAIT
EOF
chmod +x $H/run.sh; chown bobtest:bobtest $H/run.sh
systemd-run -p User=bobtest -p Delegate=yes --wait --pipe --quiet -- $H/run.sh 2>&1 | tee $S/tmp/part1.log
