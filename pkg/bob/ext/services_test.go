package ext

import (
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

func TestServices(t *testing.T) {
	var problems []string
	old := graph.Misuse
	graph.Misuse = func(msg string) { problems = append(problems, msg) }
	defer func() { graph.Misuse = old }()

	pg := &bob.ServiceSpec{Name: "postgres", Start: []string{"pg", "$ADDR"}, Ready: bob.TCPReady(5432),
		Env: map[string]string{"PGHOST": "$ADDR"}}
	api := &bob.ServiceSpec{Name: "api-server", Start: []string{"api"}, Ready: bob.CmdReady("probe", "x"), Timeout: 5 * time.Second}
	got, deps := Services([]bob.Service{pg, api}, nil)
	if len(problems) != 0 || len(got) != 2 || len(deps) != 0 {
		t.Fatalf("Services = %+v %v, problems %v", got, deps, problems)
	}
	if g := got[0]; g.Name != "postgres" || g.ReadyPort != 5432 || g.TimeoutMs != 30000 || g.Env["PGHOST"] != "$ADDR" || len(g.Tools) != 0 {
		t.Errorf("postgres IR %+v", g)
	}
	if g := got[1]; g.ReadyCmd[0] != "probe" || g.ReadyPort != 0 || g.TimeoutMs != 5000 {
		t.Errorf("api IR %+v", g)
	}

	bad := []bob.Service{
		nil,
		&bob.ServiceSpec{Name: "x", Ready: bob.TCPReady(1)},
		&bob.ServiceSpec{Name: "y", Start: []string{"y"}},
		&bob.ServiceSpec{Name: "a.b", Start: []string{"a"}, Ready: bob.TCPReady(1)},
		&bob.ServiceSpec{Name: "a-b", Start: []string{"a"}, Ready: bob.TCPReady(1)},
		&bob.ServiceSpec{Name: "../z", Start: []string{"z"}, Ready: bob.TCPReady(1)},
	}
	got, _ = Services(bad, nil)
	if len(got) != 1 || got[0].Name != "a.b" {
		t.Errorf("valid services of the bad list: %+v", got)
	}
	want := []string{"service 0 is nil", "service x has no start command", "service y needs a readiness probe",
		"services a.b and a-b would both export A_B_ADDR", `invalid name "../z"`}
	all := strings.Join(problems, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("problems lack %q:\n%s", w, all)
		}
	}
}
