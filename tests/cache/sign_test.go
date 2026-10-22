package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
)

// fixturePaths builds four store paths in a scratch store (the last three with references)
// and returns them.
func fixturePaths(t *testing.T, l local) []string {
	t.Helper()
	const pre = `let mk = name: body: derivation { inherit name; system = builtins.currentSystem; builder = "/bin/sh"; args = ["-c" body]; };
 a = mk "bob-sign-a" "echo a > $out";
 b = mk "bob-sign-b" "echo ${a} > $out";
 c = mk "bob-sign-c" "echo ${b} ${a} > $out";
 d = mk "bob-sign-d" "echo d > $out"; in `
	var out []string
	for _, v := range []string{"a", "b", "c", "d"} {
		r := l.run("build", "--impure", "--no-link", "--print-out-paths", "--store", l.store(), "--expr", pre+v)
		if r.Exit != 0 {
			t.Fatalf("harness error: nix build %s: %s", v, r.Combined())
		}
		out = append(out, strings.TrimSpace(r.Stdout))
	}
	return out
}

func TestRREG002SignatureEqualsNixStoreSign(t *testing.T) {
	parity.Covers(t, "R-REG-002")
	n := clients.Nix(t)
	l := newLocal(n)
	kr := l.run("key", "generate-secret", "--key-name", "bobtest-1")
	if kr.Exit != 0 {
		t.Fatalf("harness error: nix key generate-secret: %s", kr.Combined())
	}
	secret := strings.TrimSpace(kr.Stdout)
	keyFile := filepath.Join(n.Dir("keys"), "secret")
	if err := os.WriteFile(keyFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	n.Own(keyFile)
	paths := fixturePaths(t, l)
	if r := l.run(append([]string{"store", "sign", "--store", l.store(), "--key-file", keyFile}, paths...)...); r.Exit != 0 {
		t.Fatalf("harness error: nix store sign: %s", r.Combined())
	}
	for _, p := range paths {
		r := l.run("path-info", "--json", "--store", l.store(), p)
		if r.Exit != 0 {
			t.Fatalf("harness error: nix path-info --json %s: %s", p, r.Combined())
		}
		info, err := parsePathInfo(r.Stdout, p)
		if err != nil {
			t.Fatalf("harness error: %v: %s", err, r.Stdout)
		}
		conv := l.run("hash", "convert", "--hash-algo", "sha256", "--to", "nix32", info.narHashSRI)
		if conv.Exit != 0 {
			t.Fatalf("harness error: nix hash convert: %s", conv.Combined())
		}
		got, err := cache.Sign(secret, cache.PathInfo{
			StorePath: p, NarHash: "sha256:" + strings.TrimSpace(conv.Stdout), NarSize: info.narSize, References: info.references,
		})
		if err != nil {
			if de, ok := err.(*diag.Error); ok && de.Code == diag.CodeNotImplemented {
				harness.RedFeature(t, "cache.Sign")
				return
			}
			t.Fatalf("cache.Sign(%s): %v", p, err)
		}
		if len(info.signatures) != 1 || got != info.signatures[0] {
			t.Errorf("signature of %s: bob %q, nix store sign %q (must be byte for byte equal)", p, got, info.signatures)
		}
	}
}

type pathInfo struct {
	narHashSRI string
	narSize    uint64
	references []string
	signatures []string
}

// parsePathInfo reads `nix path-info --json` of one path in either output shape (a list, or a
// map keyed by store path).
func parsePathInfo(js, path string) (pathInfo, error) {
	type entry struct {
		NarHash    string   `json:"narHash"`
		NarSize    uint64   `json:"narSize"`
		References []string `json:"references"`
		Signatures []string `json:"signatures"`
	}
	var e entry
	var m map[string]entry
	var l []entry
	switch {
	case json.Unmarshal([]byte(js), &m) == nil && len(m) > 0:
		e = m[path]
	case json.Unmarshal([]byte(js), &l) == nil && len(l) > 0:
		e = l[0]
	default:
		return pathInfo{}, fmt.Errorf("unrecognised path-info --json output")
	}
	refs := make([]string, 0, len(e.References))
	for _, r := range e.References {
		if !strings.HasPrefix(r, "/") {
			r = "/nix/store/" + r
		}
		refs = append(refs, r)
	}
	return pathInfo{narHashSRI: e.NarHash, narSize: e.NarSize, references: refs, signatures: e.Signatures}, nil
}
