package orchestrator_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
)

func TestStagedKeys(t *testing.T) {
	const pre = "staging/b1/j1/2/"
	h := strings.Repeat("a", 32)
	dg := "sha256:" + strings.Repeat("0", 64)
	ok := `{"v":1,"paths":["/nix/store/` + h + `-api-1.4.2"],"images":[{"manifest":"` + dg + `","blobs":["` + dg + `"]}],"assets":[{"file":"api.AppImage"}]}`
	keys, err := orchestrator.StagedKeys(pre, []byte(ok), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{pre + "nar/" + h + ".nar.zst", pre + "narinfo/" + h + ".narinfo", pre + "oci/blobs/sha256/" + strings.Repeat("0", 64),
		pre + "oci/blobs/sha256/" + strings.Repeat("0", 64), pre + "COMPLETE.json"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if keys, _ := orchestrator.StagedKeys(pre, []byte(ok), true); !slices.Contains(keys, pre+"assets/api.AppImage") || keys[len(keys)-1] != pre+"COMPLETE.json" {
		t.Errorf("with assets: %v", keys)
	}
	for name, c := range map[string]struct{ prefix, doc string }{
		"prefix escapes":  {"staging/../x/", ok},
		"prefix shape":    {"nix/nar/", ok},
		"path traversal":  {pre, `{"paths":["/nix/store/../../etc"]}`},
		"bad hash":        {pre, `{"paths":["/nix/store/` + strings.Repeat("e", 32) + `-x"]}`},
		"bad digest":      {pre, `{"images":[{"manifest":"sha256:../../x"}]}`},
		"asset traversal": {pre, `{"assets":[{"file":"../COMPLETE.json"}]}`},
		"not json":        {pre, `{`},
	} {
		if _, err := orchestrator.StagedKeys(c.prefix, []byte(c.doc), true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := orchestrator.StagedKeys(pre, make([]byte, orchestrator.MaxComplete+1), false); err == nil {
		t.Errorf("oversized COMPLETE.json accepted")
	}
}

func TestCheckRecord(t *testing.T) {
	enc := func(v any) []byte {
		b, err := cbor.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, c := range map[string]struct {
		role, key string
		body      []byte
		want      string // "" | "corrupt" | "invalid"
	}{
		"job ok":            {"fleet", "job/j1", enc(store.JobRecord{State: "running", Owner: "w", Epoch: 1, Spec: []byte{1}}), ""},
		"job no owner":      {"fleet", "job/j1", enc(store.JobRecord{State: "claimed", Spec: []byte{1}}), "invalid"},
		"job bad state":     {"fleet", "job/j1", enc(store.JobRecord{State: "weird", Spec: []byte{1}}), "invalid"},
		"job garbage":       {"fleet", "job/j1", []byte{0xff, 0x00}, "corrupt"},
		"worker ok":         {"fleet", "worker/w", enc(store.WorkerRecord{State: "active"}), ""},
		"member twice":      {"fleet", "fleet/cfg", enc(store.FleetConfig{Members: []store.FleetMember{{Orch: "a"}, {Orch: "a"}}}), "invalid"},
		"lease epoch 0":     {"fleet", "ctl/x", enc(store.ControllerLease{Owner: "a"}), "invalid"},
		"build ok":          {"control", "build/b", enc(store.BuildRecord{State: "succeeded"}), ""},
		"build bad state":   {"control", "build/b", enc(store.BuildRecord{State: "x"}), "invalid"},
		"other well-formed": {"control", "pidx/all/p/0000000000", enc(map[string]int{"n": 1}), ""},
		"other truncated":   {"control", "pidx/all/p/0000000000", []byte{0xa1, 0x61}, "corrupt"},
	} {
		err := orchestrator.CheckRecord(c.role, c.key, c.body)
		got := ""
		if err != nil {
			got, _, _ = strings.Cut(err.Error(), ":")
		}
		if got != c.want {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
