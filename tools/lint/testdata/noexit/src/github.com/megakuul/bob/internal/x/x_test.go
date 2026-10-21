package x

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(m.Run()) }

func helper() {
	os.Exit(1) // want `os.Exit outside func main`
}
