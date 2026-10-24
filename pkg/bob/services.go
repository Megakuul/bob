package bob

import "time"

// Service is a loopback service a test starts before it runs (R-FW-025). It is sealed: only
// constructors build it.
type Service interface{ serviceSpec() *ServiceSpec }

// ServiceSpec describes one service.
type ServiceSpec struct {
	Name    string            // "postgres"; used in errors and logs
	Start   []string          // argv inside the test sandbox
	Tools   []Tool            // on PATH of Start
	Ready   Probe             // TCPReady(port) | CmdReady(argv...)
	Env     map[string]string // exported to the test
	Timeout time.Duration     // readiness timeout, default 30 s
}

// Probe decides when a service is ready.
type Probe struct {
	port int
	argv []string
}

// TCPReady is ready once the service accepts TCP connections on port.
func TCPReady(port int) Probe { return Probe{port: port} }

// CmdReady is ready once argv exits 0.
func CmdReady(argv ...string) Probe { return Probe{argv: argv} }

// ServiceOptions configures ServiceFrom.
type ServiceOptions struct {
	Args    []string
	Env     map[string]string
	Ready   Probe
	Timeout time.Duration
}

// service is the Service of ServiceFrom.
type service struct{ spec ServiceSpec }

func (s *service) serviceSpec() *ServiceSpec { return &s.spec }

// ServiceFrom runs a binary built in this repo as a service of a test. The service is named
// after the binary's package and runs its main program (an Executable) or bin/<name> with
// o.Args.
func ServiceFrom(bin Tool, o ServiceOptions) Service {
	if isNil(bin) {
		return &service{}
	}
	name := bin.Label().Name
	prog := "bin/" + name
	if e, ok := bin.(Executable); ok && e.MainProgram() != "" {
		prog = e.MainProgram()
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &service{spec: ServiceSpec{
		Name:    name,
		Start:   append([]string{prog}, o.Args...),
		Tools:   []Tool{bin},
		Ready:   o.Ready,
		Env:     o.Env,
		Timeout: timeout,
	}}
}
