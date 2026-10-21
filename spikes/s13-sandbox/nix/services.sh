# S13 part 2 runner (inside bob's namespace or with root Nix). $1 = label
cd "$(dirname "$0")"
run() { s=$(date +%s%N); nix build --no-link -L -f services.nix "$@" 2>&1 | grep -E '> (TIME|probe|[0-9]|A pids|B |leaving|ok|.*error|.*FATAL)' ; echo "  nix build $* wall: $(( ($(date +%s%N)-s)/1000000 )) ms"; }
echo "== postgres"; run pg
echo "== postgres x2 in parallel, both on port 5432"; run pg pg2
echo "== postgres left running when the builder exits"; run pgLeft
echo "leftover postgres processes on host: $(pgrep -u $(id -u) -c postgres || true)"
echo "== http on 127.0.0.1, 127.0.0.2, ::1"; run http
echo "== isolation between concurrent builds"; run isoA isoB
