package safe

import (
	"errors"
	"testing"
)

func TestGoRecovers(t *testing.T) {
	sentinel := errors.New("x")
	cases := []struct {
		name string
		fn   func() error
		want error
	}{
		{"ok", func() error { return nil }, nil},
		{"error", func() error { return sentinel }, sentinel},
		{"panic", func() error { panic("boom") }, ErrPanic},
	}
	for _, c := range cases {
		err := <-Go(c.fn)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: Go = %v, want %v", c.name, err, c.want)
		}
	}
}
