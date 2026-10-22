package buildctl

import (
	"testing"

	"github.com/megakuul/bob/internal/store"
)

func TestSelectionVersion(t *testing.T) {
	for _, c := range []struct {
		name, pipelines, ref, version, want string
		targets                             []string
	}{
		{name: "tag build", ref: "refs/tags/v1.4.2", want: "1.4.2"},
		{name: "rerun of a tag build", pipelines: "release", ref: "refs/tags/v1.4.2", want: "1.4.2"},
		{name: "remote --version", version: "2.0.0", targets: []string{"//rel:api"}, want: "2.0.0"},
		{name: "push", ref: "refs/heads/main"},
		{name: "tag that is no version", ref: "refs/tags/release/v1"},
	} {
		if got := selection(c.pipelines, c.targets, c.ref, false, nil, "", c.version).Version; got != c.want {
			t.Errorf("%s: version %q, want %q", c.name, got, c.want)
		}
	}
	r := Request{Project: "acme", Targets: []string{"//rel:api"}, Source: store.BuildSource{Upload: "x"}, Version: "not a version"}
	if err := r.validate(); err == nil {
		t.Errorf("invalid version accepted")
	}
}
