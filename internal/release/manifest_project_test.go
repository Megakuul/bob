package release

import (
	"encoding/json"
	"strings"
	"testing"
)

// A manifest built without a server project (project "") names its image repositories
// relative to the project; the server puts the building project in front (D-IB20-1).
func TestParseManifestWithoutProject(t *testing.T) {
	m := Manifest{Label: "//deploy:api", Project: "", Release: "api", Schema: Schema, Version: "1.4.2", Artifacts: []Artifact{{
		Kind: "oci", Name: "api-image", Platform: "multi", StorePath: "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-image",
		Repo: "api", Index: "sha256:" + strings.Repeat("a", 64), Tags: []string{"1.4.2"}}}}
	b, _ := json.Marshal(m)
	got, err := ParseManifest(b, "acme", "api")
	if err != nil || got.Artifacts[0].Repo != "acme/api" {
		t.Fatalf("%+v %v", got, err)
	}
	m.Project = "other"
	b, _ = json.Marshal(m)
	if _, err := ParseManifest(b, "acme", "api"); err == nil {
		t.Errorf("a manifest of another project was accepted")
	}
}
