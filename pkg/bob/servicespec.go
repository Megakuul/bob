package bob

import (
	"maps"
	"slices"
)

// serviceSpec makes a *ServiceSpec a Service: service packages such as pkg/services/postgres
// return &bob.ServiceSpec{…} from their constructor.
func (s *ServiceSpec) serviceSpec() *ServiceSpec { return s }

// SpecOf returns a copy of the spec of s, or nil for a nil Service. Ecosystem test
// constructors read it through ext.Services.
func SpecOf(s Service) *ServiceSpec {
	if isNil(s) {
		return nil
	}
	sp := s.serviceSpec()
	if sp == nil {
		return nil
	}
	c := *sp
	c.Start = slices.Clone(sp.Start)
	c.Tools = slices.Clone(sp.Tools)
	c.Env = maps.Clone(sp.Env)
	c.Ready = Probe{port: sp.Ready.port, argv: slices.Clone(sp.Ready.argv)}
	return &c
}

// Port is the port of a TCPReady probe (0 otherwise).
func (p Probe) Port() int { return p.port }

// Argv is the command of a CmdReady probe (nil otherwise).
func (p Probe) Argv() []string { return slices.Clone(p.argv) }
