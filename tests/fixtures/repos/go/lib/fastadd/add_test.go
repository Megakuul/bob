package fastadd

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(40, 2); got != 42 {
		t.Fatalf("Add(40, 2) = %d", got)
	}
}
