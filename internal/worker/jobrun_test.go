package worker

import (
	"slices"
	"testing"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/ir"
)

func TestReleaseLabels(t *testing.T) {
	g := &ir.Graph{Nodes: []*ir.Node{
		{Kind: "go.binary", Label: "//cmd/api:api"},
		{Kind: build.KindRelease, Label: "//deploy:api"},
		{Kind: build.KindRelease, Label: "//deploy:cli"},
	}}
	ps := []jobspec.Pipeline{{Name: "release", Releases: []string{"api"}}, {Name: "other", Releases: []string{"//deploy:cli"}}}
	if got := releaseLabels(g, ps, []string{"release"}); !slices.Equal(got, []string{"//deploy:api"}) {
		t.Errorf("by name: %v", got)
	}
	if got := releaseLabels(g, ps, []string{"other", "release"}); !slices.Equal(got, []string{"//deploy:api", "//deploy:cli"}) {
		t.Errorf("by label: %v", got)
	}
	if got := releaseLabels(g, ps, nil); len(got) != 0 {
		t.Errorf("nothing matched: %v", got)
	}
}
