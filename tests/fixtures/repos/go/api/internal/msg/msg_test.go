package msg

import "testing"

func TestSize(t *testing.T) {
	n := New("hi", 1)
	// id: 2+36, body: 2+2, checksum: 1+1
	if got := Size(n); got != 44 {
		t.Fatalf("Size = %d", got)
	}
}
