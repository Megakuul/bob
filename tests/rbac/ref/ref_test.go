package ref

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/serverenv"
)

func protoDir(t *testing.T) string {
	t.Helper()
	root, err := serverenv.Root()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "proto")
}

func TestScanner(t *testing.T) {
	c, err := Load(protoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Methods) < 90 {
		t.Fatalf("scanner found only %d methods", len(c.Methods))
	}
	m, ok := c.Find("bob.build.v1.BuildService/CancelBuild")
	if !ok || m.Kind != KindProject || m.ProjectField != "id" || m.Resolve != "BUILD" {
		t.Fatalf("CancelBuild = %+v", m)
	}
	m, _ = c.Find("bob.auth.v1.AuthService/WhoAmI")
	if m.Kind != KindAuthenticated || !m.AllowDownload || !m.GetOK {
		t.Fatalf("WhoAmI = %+v", m)
	}
	if p := c.Problems(); len(p) != 0 {
		t.Fatalf("unexpected rule problems: %v", p)
	}
	for path, mname := range map[string]string{"project.id": "bob.project.v1.ProjectService/UpdateProject", "release.id": "bob.release.v1.ReleaseService/UpdateRelease"} {
		m, ok := c.Find(mname)
		if !ok || m.ProjectField != path {
			t.Fatalf("%s project_field = %q, want %q", mname, m.ProjectField, path)
		}
	}
	m, _ = c.Find("bob.project.v1.ProjectService/UpdateProject")
	for path, want := range map[string]bool{"project.id": true, "project.nope": false, "project": false, "update_mask.paths": false, "project.id.x": false} {
		typ, ok := c.PathField(m, path)
		if got := ok && typ == "string"; got != want {
			t.Errorf("PathField(%q) = %q, %v; string field = %v, want %v", path, typ, ok, got, want)
		}
	}
}

func TestFixtureWithoutRulesFails(t *testing.T) {
	c, err := Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(c.Problems(), "\n")
	for _, want := range []string{
		"Open: no bob.authz.v1.rule", "Unspec: rule kind is UNSPECIFIED",
		`BadField: project_field "missing"`, `RepeatedField: project_field "many"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Problems() lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Fine") {
		t.Errorf("Problems() flags the valid method:\n%s", got)
	}
}

func TestEffective(t *testing.T) {
	cases := []struct {
		bind, rp string
		want     string // "" = none
	}{
		{"*", "p1", "p1"}, {"*", "*", "*"}, {"p1", "*", "p1"}, {"p1", "p1", "p1"}, {"p1", "p2", ""}, {"p2", "p1", ""},
	}
	for _, c := range cases {
		got := Effective(Binding{Permissions: []Permission{{"*", c.rp}}, Project: c.bind})
		switch {
		case c.want == "" && len(got) != 0, c.want != "" && (len(got) != 1 || got[0].Project != c.want):
			t.Errorf("bind %s rp %s: got %v want %q", c.bind, c.rp, got, c.want)
		}
	}
}
