package server

import "testing"

// TestOutboundAllowConfig (I-R2 F8): server.outbound_allow is a comma-separated list of hosts,
// addresses and CIDR ranges; anything else refuses the start.
func TestOutboundAllowConfig(t *testing.T) {
	env := func(v string) func(string) string {
		m := map[string]string{"BOB_STORAGE_ENDPOINT": "http://127.0.0.1:9000", "BOB_SERVER_NAME": "o1", "BOB_SERVER_OUTBOUND_ALLOW": v}
		return func(k string) string { return m[k] }
	}
	c, err := ConfigFromEnv(ModeOrchestrator, env(" forge.corp.example, 10.0.0.0/8 ,fd00::1"))
	if err != nil || len(c.OutboundAllow) != 3 || c.OutboundAllow[1] != "10.0.0.0/8" {
		t.Errorf("config = %+v, %v", c.OutboundAllow, err)
	}
	if _, err := ConfigFromEnv(ModeOrchestrator, env("https://forge.corp.example")); !IsConfigError(err) {
		t.Errorf("a URL entry: %v, want a ConfigError", err)
	}
}
