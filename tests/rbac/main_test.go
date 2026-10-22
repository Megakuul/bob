package rbac

import (
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/rbac/ref"
	"github.com/megakuul/bob/tests/serverenv"
)

func TestMain(m *testing.M) { authenv.Main(m) }

func catalog(t *testing.T) *ref.Catalog {
	t.Helper()
	root, err := serverenv.Root()
	if err != nil {
		t.Fatal(err)
	}
	c, err := ref.Load(filepath.Join(root, "proto"))
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	return c
}
