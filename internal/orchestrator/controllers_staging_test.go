package orchestrator_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/internal/orchestrator"
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
