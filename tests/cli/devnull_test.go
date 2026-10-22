package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
)

// zshSourceArgs is how TestRCLI004Scripts sources a completion script in zsh: compinit
// without a dump file (-D). The dump file must never be /dev/null: compdump writes
// <dumpfile>.$HOST.$$ next to it and `mv -f`s that over the dump file, which as root replaced
// the host's /dev/null with a regular file twice (I-X1).
func zshSourceArgs(script string) []string {
	return []string{"-f", "-c", "autoload -Uz compinit && compinit -u -D; source " + script}
}

// devNullProbe runs zsh with args inside a private mount namespace whose /dev is a fresh tmpfs
// holding only a null device, and returns what that /dev/null is afterwards ("character
// special file" or "regular file"). The host's /dev is never written: the script stops before
// zsh unless /dev is the namespace's own empty tmpfs.
func devNullProbe(t *testing.T, zsh string, args []string) string {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil || os.Geteuid() != 0 {
		t.Fatalf("harness error: needs root and unshare(1) for a private /dev: %v", err)
	}
	home := t.TempDir()
	script := `set -eu
mount -t tmpfs -o mode=755,size=1m none /dev
[ "$(stat -f -c %T /dev)" = tmpfs ] || { echo "no private /dev" >&2; exit 97; }
[ -z "$(ls -A /dev)" ] || { echo "the private /dev is not empty" >&2; exit 97; }
mknod -m 666 /dev/null c 1 3
"$0" "$@" </dev/null >"$HOME/zsh.out" 2>&1 || true
stat -c %F /dev/null`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, unshare, append([]string{"--mount", "--propagation", "private", "sh", "-c", script, zsh}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TERM=dumb"}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		zout, _ := os.ReadFile(filepath.Join(home, "zsh.out"))
		t.Fatalf("harness error: private /dev probe: %v\n%s\nzsh: %s", err, stderr.String(), zout)
	}
	return strings.TrimSpace(string(out))
}

// Sourcing the completion script in zsh the way TestRCLI004Scripts does leaves /dev/null the
// null device (I-X1). The control is the command line that clobbered the host twice: inside the
// private /dev it turns /dev/null into a regular file, which proves the probe sees the damage.
func TestZshCompletionCheckKeepsDevNull(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatalf("harness error: zsh is not on PATH (the pinned shells of HARNESS §1.4 are required): %v", err)
	}
	script := filepath.Join(t.TempDir(), "bob-completion.zsh")
	if err := os.WriteFile(script, []byte("_bob() { :; }\ncompdef _bob bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clobbering := []string{"-f", "-c", "autoload -Uz compinit && compinit -u -d /dev/null; source " + script}
	if got := devNullProbe(t, zsh, clobbering); got != "regular file" {
		t.Fatalf("control: `compinit -d /dev/null` left the private /dev/null as %q, want \"regular file\": the probe does not reproduce I-X1", got)
	}
	if got := devNullProbe(t, zsh, zshSourceArgs(script)); got != "character special file" {
		t.Errorf("sourcing a zsh completion script turned /dev/null into a %s (I-X1)", got)
	}
	if err := harness.CheckDevNull(); err != nil {
		t.Fatalf("HOST DAMAGE: %v", err)
	}
}
