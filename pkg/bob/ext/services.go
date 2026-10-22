package ext

import (
	"fmt"
	"maps"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

// Service is the IR form of one loopback service of a test (R-FW-025), part of a test kind's
// spec. Tools are indices into the node's deps; the generator turns them into the PATH of the
// builder's test-run service. Instance i of the list binds 127.0.0.(i+2).
type Service struct {
	Name      string            `json:"name"`
	Start     []string          `json:"start"`
	Tools     []int             `json:"tools"`
	ReadyPort int               `json:"readyPort,omitempty"`
	ReadyCmd  []string          `json:"readyCmd,omitempty"`
	Env       map[string]string `json:"env"`
	TimeoutMs int64             `json:"timeoutMs"`
}

// defaultServiceTimeout is the readiness timeout of a service that sets none (R-FW-025).
const defaultServiceTimeout = 30 * time.Second

var serviceName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`) })

// Services returns the IR form of a test's services and deps with the services' tools
// appended. Problems (a nil service, no start command, not exactly one probe, two services
// with the same address variable) are definition errors of the calling constructor.
//
//	svcs, deps := ext.Services(o.Services, deps)
//	spec := testSpec{…, Services: svcs}
//	return &TestSet{Node: ext.New(p, "go.test", name, deps, srcs, spec)}
func Services(services []bob.Service, deps []bob.Package) ([]Service, []bob.Package) {
	out := make([]Service, 0, len(services))
	vars := map[string]string{}
	for i, s := range services {
		sp := bob.SpecOf(s)
		if sp == nil {
			misuse(fmt.Sprintf("service %d is nil", i))
			continue
		}
		problem := ""
		port, cmd := sp.Ready.Port(), sp.Ready.Argv()
		switch {
		case !serviceName().MatchString(sp.Name):
			problem = fmt.Sprintf("service %d has the invalid name %q (letters, digits, '.', '_', '-')", i, sp.Name)
		case len(sp.Start) == 0 || sp.Start[0] == "":
			problem = fmt.Sprintf("service %s has no start command", sp.Name)
		case (port == 0) == (len(cmd) == 0):
			problem = fmt.Sprintf("service %s needs a readiness probe (bob.TCPReady or bob.CmdReady)", sp.Name)
		case port < 0 || port > 65535:
			problem = fmt.Sprintf("service %s probes port %d", sp.Name, port)
		case sp.Timeout < 0:
			problem = fmt.Sprintf("service %s has a negative timeout", sp.Name)
		}
		if v := addrVar(sp.Name); problem == "" {
			if other := vars[v]; other != "" {
				problem = fmt.Sprintf("services %s and %s would both export %s", other, sp.Name, v)
			}
			vars[v] = sp.Name
		}
		if problem != "" {
			misuse(problem)
			continue
		}
		timeout := sp.Timeout
		if timeout == 0 {
			timeout = defaultServiceTimeout
		}
		ir := Service{Name: sp.Name, Start: sp.Start, Tools: []int{}, ReadyPort: port, ReadyCmd: cmd,
			Env: maps.Clone(sp.Env), TimeoutMs: timeout.Milliseconds()}
		if ir.Env == nil {
			ir.Env = map[string]string{}
		}
		for j, t := range sp.Tools {
			if t == nil {
				misuse(fmt.Sprintf("service %s: tool %d is nil", sp.Name, j))
				continue
			}
			ir.Tools = append(ir.Tools, len(deps))
			deps = append(deps, t)
		}
		out = append(out, ir)
	}
	return out, deps
}

// addrVar is the variable that carries a service's address, as bob-builder exports it.
func addrVar(name string) string {
	b := []byte(strings.ToUpper(name))
	for i, ch := range b {
		if !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			b[i] = '_'
		}
	}
	return string(b) + "_ADDR"
}

func misuse(msg string) {
	if graph.Misuse != nil {
		graph.Misuse("ext.Services: " + msg)
	}
}
