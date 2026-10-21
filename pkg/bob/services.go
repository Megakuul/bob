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

// ServiceFrom runs a binary built in this repo as a service of a test.
func ServiceFrom(bin Tool, o ServiceOptions) Service { return nil }
