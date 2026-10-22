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
	// Known proto problem (reported to the coordinator): these two rules name "id" but the
	// request only has the nested project.id / release.id. The acceptance test of
	// R-AUTH-006 (tests/rbac) demands zero problems and stays failing until proto/ is fixed.
	if p := c.Problems(); len(p) != 2 || !strings.Contains(p[0], "UpdateProject") || !strings.Contains(p[1], "UpdateRelease") {
		t.Fatalf("unexpected rule problems: %v", p)
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
