package install

import (
	"testing"

	"github.com/megakuul/bob/tests/harness"
)

// The suite runs bash and the installer as root; harness.Main fails it when /dev/null was
// clobbered (I-X1).
func TestMain(m *testing.M) { harness.Main(m) }
