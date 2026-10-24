package api

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
)

// The worker refusal sentinels are mapped with errors.Is, so a wrapping prefix added on the way
// (here "job j1: ") still gives the right code and the reason after the sentinel.
func TestWorkerErrorSentinels(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		code connect.Code
		msg  string
	}{
		"unauthenticated":         {fmt.Errorf("%w: token expired", ErrWorkerUnauthenticated), connect.CodeUnauthenticated, "token expired"},
		"permission denied":       {fmt.Errorf("%w: worker holds no lease on job j1", ErrWorkerPermissionDenied), connect.CodePermissionDenied, "worker holds no lease on job j1"},
		"wrapped unauthenticated": {fmt.Errorf("job j1: %w", fmt.Errorf("%w: replayed", ErrWorkerUnauthenticated)), connect.CodeUnauthenticated, "replayed"},
		"wrapped denied":          {fmt.Errorf("checkout: %w", fmt.Errorf("%w: worker holds no lease on job j2", ErrWorkerPermissionDenied)), connect.CodePermissionDenied, "worker holds no lease on job j2"},
	} {
		var ce *connect.Error
		if got := workerError(c.err); !errors.As(got, &ce) || ce.Code() != c.code || ce.Message() != c.msg {
			t.Errorf("%s: workerError = %v, want %v %q", name, got, c.code, c.msg)
		}
	}
	plain := errors.New("store down")
	if got := workerError(plain); got != plain {
		t.Errorf("plain error mapped to %v, want it unchanged", got)
	}
	if got := WorkerPermissionDenied(fmt.Errorf("x: %w: no lease", ErrWorkerPermissionDenied)); connect.CodeOf(got) != connect.CodePermissionDenied {
		t.Errorf("WorkerPermissionDenied = %v", got)
	}
}
