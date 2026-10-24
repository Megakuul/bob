package cache

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// sigNames returns the key names of the Sig lines of the narinfo of path.
func sigNames(t *testing.T, e *regenv.Env, path string) []string {
	t.Helper()
	code, _, body := e.Get(e.API+"/nix/"+regenv.StoreHash(path)+".narinfo", e.Admin, nil)
	if code != http.StatusOK {
		t.Fatalf("narinfo of %s: HTTP %d", path, code)
	}
	var out []string
	for _, l := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(l, "Sig: "); ok {
			name, _, _ := strings.Cut(v, ":")
			out = append(out, name)
		}
	}
	return out
}

func keyName(k string) string { n, _, _ := strings.Cut(k, ":"); return n }

func TestRREG153SigningKeyRotation(t *testing.T) {
	parity.Covers(t, "R-REG-153")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	_, oldRoot := publishP1(t, e)
	oldKeys := e.PublicKeys()
	if len(oldKeys) != 1 {
		t.Fatalf("a fresh server has %d signing keys, want 1", len(oldKeys))
	}
	oldName := keyName(oldKeys[0])

	var created struct {
		Key struct{ Name, PublicKey string }
	}
	e.MustCall(e.Admin, "bob.settings.v1.SettingsService/CreateSigningKey", nil, &created)
	newName, newKey := created.Key.Name, created.Key.PublicKey
	if newName == "" || newName == oldName {
		t.Fatalf("CreateSigningKey returned key %q (old %q)", newName, oldName)
	}

	// promote a new path (the fixture changes one source file)
	dir := fixtureDir(t)
	f := filepath.Join(dir, "cmd", "api", "main.go")
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append(b, []byte("\n// rotation\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=a", "-c", "user.email=a@b", "commit", "-m", "rotation"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("harness error: git %v: %s", args, out)
		}
	}
	e.Push(dir, "demo", "main")
	e.Tag(dir, "demo", "v1.4.3")
	rel := e.WaitRelease("acme", "api", "1.4.3", 20*time.Minute)
	newRoot := roots(t, rel)[0]

	if got := sigNames(t, e, newRoot); len(got) != 1 || got[0] != newName {
		t.Errorf("path promoted after rotation carries signatures from %v, want only %s", got, newName)
	}
	n := clients.Nix(t)
	l := newLocal(n)
	netrc := regenv.Netrc(t, n, e.API, e.Admin)
	if r := l.copyFrom(e.API+"/nix", netrc, []string{oldKeys[0], newKey}, oldRoot); r.Exit != 0 {
		t.Fatalf("nix copy of the old closure: %s", r.Combined())
	}
	if v := l.verify([]string{oldKeys[0]}, oldRoot); v.Exit != 0 {
		t.Errorf("old path no longer verifies with the old key: %s", v.Combined())
	}

	// retire the old key: after the sweep every narinfo verifies with the new key alone
	e.MustCall(e.Admin, "bob.settings.v1.SettingsService/RetireSigningKey", map[string]any{"name": oldName}, nil)
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) {
		got := sigNames(t, e, oldRoot)
		if len(got) == 1 && got[0] == newName {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the re-sign sweep left signatures %v on the old path after 3 minutes", got)
		}
	}
	l = newLocal(n)
	if r := l.copyFrom(e.API+"/nix", netrc, []string{newKey}, oldRoot, newRoot); r.Exit != 0 {
		t.Fatalf("nix copy with the new key alone: %s", r.Combined())
	}
	all := append(l.closure(t, oldRoot), l.closure(t, newRoot)...)
	if v := l.verify([]string{newKey}, all...); v.Exit != 0 {
		t.Errorf("nix store verify with the new key alone: %s", v.Combined())
	}
	var who struct{ Nix struct{ PublicKeys []string } }
	e.MustCall(e.Admin, "bob.auth.v1.AuthService/WhoAmI", nil, &who)
	for _, k := range who.Nix.PublicKeys {
		if keyName(k) == oldName {
			t.Errorf("WhoAmI.nix.public_keys still lists the retired key %s", oldName)
		}
	}
	// The If-Match rewrite of the narinfo is a request header the counting proxy does not record
	// (SPEC problem, see the report); chaos suites cover the CAS behaviour (R-STORE).
	e.AssertNoList()
}
