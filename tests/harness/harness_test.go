package harness

import "testing"

func TestStubIsRecognised(t *testing.T) {
	r := Run(t, "bob", Spec{}, "build", "//...")
	if !r.IsNotImplemented() {
		t.Logf("bob build is implemented now (exit %d); the helper still works", r.Exit)
	}
}

func TestFixtureCopy(t *testing.T) {
	dir := Fixture(t, "acme")
	r := RunPath(t, "/usr/bin/git", Spec{Dir: dir}, "status", "--porcelain")
	if r.Exit != 0 || r.Stdout != "" {
		t.Fatalf("fixture not clean: exit %d %q %q", r.Exit, r.Stdout, r.Stderr)
	}
}
