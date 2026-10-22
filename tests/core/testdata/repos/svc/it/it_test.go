package it

import (
	"net"
	"os"
	"testing"
	"time"
)

// TestPostgresAnswersSSLRequest talks the first bytes of the Postgres wire protocol: an
// SSLRequest is answered with 'S' or 'N'.
func TestPostgresAnswersSSLRequest(t *testing.T) {
	addr := os.Getenv("POSTGRES_ADDR")
	if addr == "" {
		t.Fatal("POSTGRES_ADDR is not set")
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(addr, "5432"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte{0, 0, 0, 8, 4, 210, 22, 47}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(buf); err != nil || (buf[0] != 'S' && buf[0] != 'N') {
		t.Fatalf("no SSLRequest answer: %v %q", err, buf)
	}
}
