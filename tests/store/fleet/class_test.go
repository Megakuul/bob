package fleet

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"testing"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

var classIDRE = regexp.MustCompile(`^(x86_64-linux|aarch64-linux)\.[a-z2-7]{10}$`)

var (
	capPool   = []string{"kvm", "gpu", "big-mem", "fuse", "nested-virt", "ssd", "avx512"}
	labelPool = []string{"team-a", "team-b", "prod", "bench", "ci", "arm-lab", "x"}
	platforms = []string{"x86_64-linux", "aarch64-linux"}
)

func subset(r *rand.Rand, pool []string) []string {
	var out []string
	for _, p := range pool {
		if r.IntN(3) == 0 {
			out = append(out, p)
		}
	}
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func has(have []string, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// TestRFLEET004ClassEligibility is the property test of R-FLEET-004 over 1,000 random cap/label
// sets: a job of class `<platform>.<h>` is never claimed by a worker of another platform or one
// that lacks a required cap or label, and workers only read the slots of classes they satisfy.
func TestRFLEET004ClassEligibility(t *testing.T) {
	parity.Covers(t, "R-FLEET-004")
	r := rand.New(rand.NewPCG(4, 4))

	// Class id: format, determinism, independence of order, distinct for distinct sets.
	ids := map[string]string{}
	for i := 0; i < 1000; i++ {
		c := store.Class{Platform: platforms[r.IntN(2)], Caps: subset(r, capPool), Labels: subset(r, labelPool)}
		id, err := store.ClassID(c)
		if env.Red(t, err) {
			return
		}
		if err != nil || !classIDRE.MatchString(id) {
			t.Fatalf("ClassID(%+v) = %q, %v; want <platform>.<10 base32 chars>", c, id, err)
		}
		if got := id[:len(c.Platform)]; got != c.Platform {
			t.Errorf("ClassID(%+v) = %q does not start with the platform", c, id)
		}
		shuffled := store.Class{Platform: c.Platform, Caps: slices.Clone(c.Caps), Labels: slices.Clone(c.Labels)}
		r.Shuffle(len(shuffled.Caps), func(a, b int) { shuffled.Caps[a], shuffled.Caps[b] = shuffled.Caps[b], shuffled.Caps[a] })
		r.Shuffle(len(shuffled.Labels), func(a, b int) { shuffled.Labels[a], shuffled.Labels[b] = shuffled.Labels[b], shuffled.Labels[a] })
		if id2, _ := store.ClassID(shuffled); id2 != id {
			t.Errorf("ClassID depends on the order of caps or labels: %q vs %q", id, id2)
		}
		key := fmt.Sprint(c.Platform, "|", slices.Sorted(slices.Values(c.Caps)), "|", slices.Sorted(slices.Values(c.Labels)))
		if prev, dup := ids[id]; dup && prev != key {
			t.Errorf("class id collision: %q for %s and %s", id, prev, key)
		}
		ids[id] = key
	}
	// Caps and labels are different axes: moving a name between them changes the id.
	a, _ := store.ClassID(store.Class{Platform: "x86_64-linux", Caps: []string{"kvm"}})
	b, _ := store.ClassID(store.Class{Platform: "x86_64-linux", Labels: []string{"kvm"}})
	if a == b {
		t.Errorf("a cap and a label of the same name give the same class id %q", a)
	}

	// Eligibility: exactly "same platform and caps ⊆ worker caps and labels ⊆ worker labels".
	eligibleN, refusedN := 0, 0
	for i := 0; i < 1000; i++ {
		c := store.Class{Platform: platforms[r.IntN(2)], Caps: subset(r, capPool), Labels: subset(r, labelPool)}
		w := store.WorkerProfile{Platform: platforms[r.IntN(2)], Caps: subset(r, capPool), Labels: subset(r, labelPool)}
		if r.IntN(4) == 0 { // make eligible pairs common enough to matter
			w = store.WorkerProfile{Platform: c.Platform, Caps: append(slices.Clone(c.Caps), subset(r, capPool)...), Labels: append(slices.Clone(c.Labels), subset(r, labelPool)...)}
		}
		want := c.Platform == w.Platform && has(w.Caps, c.Caps) && has(w.Labels, c.Labels)
		got, err := store.Satisfies(w, c)
		if err != nil || got != want {
			t.Fatalf("Satisfies(worker %+v, class %+v) = %v, %v; want %v", w, c, got, err, want)
		}
		if want {
			eligibleN++
		} else {
			refusedN++
		}
	}
	if eligibleN < 100 || refusedN < 100 {
		t.Fatalf("the property test is degenerate: %d eligible, %d refused pairs", eligibleN, refusedN)
	}
}
