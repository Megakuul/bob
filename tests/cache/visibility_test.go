package cache

import (
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// servedPaths substitutes the closure of rel's first root into a scratch store and returns the
// store hashes of every path bob serves to the token (the narinfo answers 200): the bob-built
// paths of that closure and every other promoted path of the token's project. The release
// closure alone holds too few bob-built paths for 10 distinct ones (fixture acme: the cgo binary
// and libfoo; C++ and Rust are linked in), and R-REG-150 asks for any 10 distinct paths.
func servedPaths(t *testing.T, e *regenv.Env, n *clients.Client, keys []string, token string, rel regenv.Release) []string {
	t.Helper()
	l := newLocal(n)
	root := roots(t, rel)[0]
	if r := l.copyFrom(e.API+"/nix", regenv.Netrc(t, n, e.API, token), keys, root); r.Exit != 0 {
		t.Fatalf("nix copy %s: exit %d: %s", root, r.Exit, r.Combined())
	}
	seen := map[string]bool{}
	var out []string
	add := func(hash string) {
		if !seen[hash] && narinfoStatus(e, e.API, hash, token) == http.StatusOK {
			seen[hash] = true
			out = append(out, hash)
		}
	}
	for _, p := range l.closure(t, root) {
		add(regenv.StoreHash(p))
	}
	for _, k := range e.S3List("nix/narinfo/") { // the backend directly, not counted by the proxy
		add(hashOfKey(k))
	}
	return out
}

func TestRREG150VisibilityDocuments(t *testing.T) {
	parity.Covers(t, "R-REG-150")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	rel, root := publishP1(t, e)
	keys := e.PublicKeys()
	n := clients.Nix(t)
	tok := e.Principal("p1-reader", "acme", "DOWNLOAD")

	paths := servedPaths(t, e, n, keys, tok, rel)
	if len(paths) < 10 {
		t.Fatalf("bob serves %d paths to the p1 token after publishing %s, the acceptance needs 10 distinct", len(paths), root)
	}
	sort.Strings(paths)
	paths = paths[:10]

	// 1,000 narinfo requests for 10 distinct paths: <= 20 GETs on nix/vis/, no LIST at all.
	e.ResetS3()
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if narinfoStatus(e, e.API, paths[(w*50+i)%10], tok) != http.StatusOK {
					mu.Lock()
					bad++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if bad != 0 {
		t.Errorf("%d of 1000 narinfo requests were not 200", bad)
	}
	gets, lists := 0, 0
	for _, r := range e.S3().Rows {
		if r.Bucket == "bob-artifacts" && r.Prefix == "nix/vis/" && r.Op == "GET" {
			gets += r.Count
		}
		if r.Op == "LIST" {
			lists += r.Count
		}
	}
	if gets > 20 {
		t.Errorf("1,000 narinfo requests caused %d GETs on nix/vis/, want <= 20", gets)
	}
	if lists != 0 {
		t.Errorf("1,000 narinfo requests caused %d LIST requests, want 0", lists)
	}

	// Making a release private hides its exclusive paths from anonymous callers within 31 s.
	e.SetVisibility(rel.ID, "PUBLIC")
	for deadline := time.Now().Add(35 * time.Second); narinfoStatus(e, e.API, root, "") != http.StatusOK; {
		if time.Now().After(deadline) {
			t.Fatalf("a public release never became anonymously visible")
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.SetVisibility(rel.ID, "PRIVATE")
	start := time.Now()
	for narinfoStatus(e, e.API, root, "") != http.StatusNotFound {
		if time.Since(start) > 31*time.Second {
			t.Fatalf("path still anonymously visible %v after the release became private, want <= 31 s", time.Since(start))
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.AssertNoList()
}
