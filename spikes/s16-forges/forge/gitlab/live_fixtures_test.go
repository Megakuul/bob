package gitlab

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bobspike/s16/forge"
)

// Real deliveries captured from GitLab 19.4.2 (fixtures/gitlab/live), verified with the
// signing token that was configured on the hook.
func TestLiveFixtures(t *testing.T) {
	dir := filepath.Join(fix, "live")
	tok, err := os.ReadFile(filepath.Join(dir, "SIGNING_TOKEN.test"))
	if err != nil {
		t.Skip("no live fixtures")
	}
	a, err := New(Config{BaseURL: "https://x", SigningToken: forge.NewSecret(string(tok))})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.body.json"))
	want := map[string]forge.EventType{"push_hook": forge.EventPush, "merge_request_hook": forge.EventPullRequest,
		"tag_push_hook": forge.EventTag, "release_hook": forge.EventIgnored}
	for _, f := range files {
		base := strings.TrimSuffix(f, ".body.json")
		body, _ := os.ReadFile(f)
		hb, _ := os.ReadFile(base + ".headers.json")
		var m map[string]string
		json.Unmarshal(hb, &m)
		h := http.Header{}
		for k, v := range m {
			h.Set(k, v)
		}
		ts, _ := strconv.ParseInt(h.Get("webhook-timestamp"), 10, 64)
		e, err := a.ParseWebhook(h, body, time.Unix(ts, 0).Add(time.Second))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		kind := strings.SplitN(filepath.Base(base), "-", 2)[1]
		if e.Type != want[kind] {
			t.Errorf("%s: type %s", filepath.Base(f), e.Type)
		}
		if e.Type == forge.EventTag && e.Tag.Commit == "" {
			t.Errorf("tag without peeled commit")
		}
	}
	if len(files) < 8 {
		t.Errorf("only %d fixtures", len(files))
	}
}
