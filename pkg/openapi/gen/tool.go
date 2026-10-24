package gen

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/megakuul/bob/pkg/engine/lock"
)

// The openapi-typescript tool lock (SPEC §03.6.5): pinned when bob is released
// (go test ./pkg/openapi/gen -run TestToolLock -update-tool-lock), installed with the npm
// installer, driven by driver.mjs through the library API (no CLI).
var (
	//go:embed toollock/openapi-typescript.json
	toolLockJSON []byte
	//go:embed toollock/driver.mjs
	driverJS string
)

// toolName names the tool in derivation names and messages.
const toolName = "openapi-typescript"

// toolDeps are the tool's direct dependencies: openapi-typescript and its peer typescript.
var toolDeps = map[string]string{
	"openapi-typescript": "7.13.0",
	"typescript":         "5.9.3",
}

// toolLock is the embedded lock, decoded once.
var toolLock = sync.OnceValues(func() (*lock.ToolLock, error) {
	dec := json.NewDecoder(bytes.NewReader(toolLockJSON))
	dec.DisallowUnknownFields()
	var tl lock.ToolLock
	if err := dec.Decode(&tl); err != nil {
		return nil, fmt.Errorf("the embedded %s tool lock: %w", toolName, err)
	}
	return &tl, nil
})
