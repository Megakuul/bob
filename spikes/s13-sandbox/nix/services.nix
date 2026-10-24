with import ./common.nix;
let
  ms = ''now() { echo $(( $(date +%s%N) / 1000000 )); }; T0=$(now); lap() { echo "TIME $1 $(( $(now) - T0 )) ms"; }'';
  pgTest = tag: stop: sh "s13-pg-${tag}" ''
    set -e
    export PATH=${pg}/bin:$PATH
    ${ms}
    initdb -D $TMPDIR/db -U test --auth=trust --no-sync -E UTF8 >/dev/null
    lap initdb
    pg_ctl -D $TMPDIR/db -l $TMPDIR/pg.log -w -o "-c listen_addresses=127.0.0.1 -p 5432 -k $TMPDIR -c fsync=off -c synchronous_commit=off -c full_page_writes=off" start >/dev/null
    lap started
    psql -h 127.0.0.1 -p 5432 -U test -d postgres -qAt -c "create table t(x int); insert into t select generate_series(1,100000); select count(*) from t;"
    lap query
    psql -h localhost -p 5432 -U test -d postgres -qAt -c "select inet_server_addr()"
    ${if stop then ''pg_ctl -D $TMPDIR/db -m fast -w stop >/dev/null; lap stopped'' else ''echo "leaving postgres running (pid $(head -1 $TMPDIR/db/postmaster.pid))"''}
    echo ok > $out
  '';
in {
  pg = pgTest "a" true;
  pg2 = pgTest "b" true;
  pgLeft = pgTest "left" false;
  http = sh "s13-http" ''
    set -e
    ${ms}
    for a in 127.0.0.1:8080 127.0.0.2:8080 '[::1]:8080'; do
      ${loopsvc} serve "$a" > $TMPDIR/svc.log 2>&1 &
      ${loopsvc} probe "$a" 1000
      kill $!; wait $! 2>/dev/null || true
    done
    lap total
    echo ok > $out
  '';
  # Two builds at the same time: A serves on 127.0.0.1:5000, B tries to reach it and looks for A's pids.
  isoA = sh "s13-iso-a" ''
    ${loopsvc} serve 127.0.0.1:5000 & 
    echo "A pids: $(ls /proc | grep -E '^[0-9]+$' | tr '\n' ' ')"
    read -t 6 <> <(:) || true
    echo ok > $out
  '';
  isoB = sh "s13-iso-b" ''
    read -t 1 <> <(:) || true
    echo "B pids: $(ls /proc | grep -E '^[0-9]+$' | tr '\n' ' ')"
    if timeout 3 ${loopsvc} probe 127.0.0.1:5000 1 >/dev/null 2>&1; then echo "B REACHED A"; else echo "B cannot reach A's 127.0.0.1:5000"; fi
    echo ok > $out
  '';
}
