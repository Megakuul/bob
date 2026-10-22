package gen

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/megakuul/bob/pkg/engine/lock"
)

// The graphql-codegen tool lock (SPEC §03.6.5): pinned when bob is released
// (go test ./pkg/graphql/gen -run TestToolLock -update-tool-lock), installed with the npm
// installer, driven by driver.mjs through the plugin API (no CLI, no codegen.yml).
var (
	//go:embed toollock/graphql-codegen.json
	toolLockJSON []byte
	//go:embed toollock/driver.mjs
	driverJS string
)

// toolName names the tool in derivation names and messages.
const toolName = "graphql-codegen"

// toolDeps are the tool's direct dependencies (versions verified in S6).
var toolDeps = map[string]string{
	"@graphql-codegen/core":                  "6.2.2",
	"@graphql-codegen/typescript":            "6.1.2",
	"@graphql-codegen/typescript-operations": "6.1.10",
	"graphql":                                "16.14.2",
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
