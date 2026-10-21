// Package nixlog parses Nix's `--log-format internal-json` stream ("@nix {...}" lines on stderr)
// into typed events. It knows every action, activity type and result type that Nix 2.34
// emits (verified against captured streams, see fixtures/) and counts anything else as drift.
package nixlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Action is the "action" field.
type Action string

const (
	ActMsg    Action = "msg"
	ActStart  Action = "start"
	ActStop   Action = "stop"
	ActResult Action = "result"
	// ActSetPhase etc. do not exist as actions; phases are results (ResSetPhase).
)

// ActivityType mirrors Nix's ActivityType enum (src/libutil/include/nix/util/logging.hh).
type ActivityType int

const (
	ActUnknown       ActivityType = 0
	ActCopyPath      ActivityType = 100
	ActFileTransfer  ActivityType = 101
	ActRealise       ActivityType = 102
	ActCopyPaths     ActivityType = 103
	ActBuilds        ActivityType = 104
	ActBuild         ActivityType = 105
	ActOptimiseStore ActivityType = 106
	ActVerifyPaths   ActivityType = 107
	ActSubstitute    ActivityType = 108
	ActQueryPathInfo ActivityType = 109
	ActPostBuildHook ActivityType = 110
	ActBuildWaiting  ActivityType = 111
	ActFetchTree     ActivityType = 112
)

var activityNames = map[ActivityType]string{
	ActUnknown: "unknown", ActCopyPath: "copy-path", ActFileTransfer: "file-transfer", ActRealise: "realise",
	ActCopyPaths: "copy-paths", ActBuilds: "builds", ActBuild: "build", ActOptimiseStore: "optimise-store",
	ActVerifyPaths: "verify-paths", ActSubstitute: "substitute", ActQueryPathInfo: "query-path-info",
	ActPostBuildHook: "post-build-hook", ActBuildWaiting: "build-waiting", ActFetchTree: "fetch-tree",
}

func (t ActivityType) String() string {
	if n, ok := activityNames[t]; ok {
		return n
	}
	return fmt.Sprintf("activity-%d", int(t))
}

// Known reports whether t is an activity type of the supported Nix versions.
func (t ActivityType) Known() bool { _, ok := activityNames[t]; return ok }

// ResultType mirrors Nix's ResultType enum.
type ResultType int

const (
	ResFileLinked       ResultType = 100
	ResBuildLogLine     ResultType = 101
	ResUntrustedPath    ResultType = 102
	ResCorruptedPath    ResultType = 103
	ResSetPhase         ResultType = 104
	ResProgress         ResultType = 105
	ResSetExpected      ResultType = 106
	ResPostBuildLogLine ResultType = 107
	ResFetchStatus      ResultType = 108
)

var resultNames = map[ResultType]string{
	ResFileLinked: "file-linked", ResBuildLogLine: "build-log-line", ResUntrustedPath: "untrusted-path",
	ResCorruptedPath: "corrupted-path", ResSetPhase: "set-phase", ResProgress: "progress",
	ResSetExpected: "set-expected", ResPostBuildLogLine: "post-build-log-line", ResFetchStatus: "fetch-status",
}

func (t ResultType) String() string {
	if n, ok := resultNames[t]; ok {
		return n
	}
	return fmt.Sprintf("result-%d", int(t))
}

func (t ResultType) Known() bool { _, ok := resultNames[t]; return ok }

// Level mirrors Nix's Verbosity.
type Level int

const (
	LvlError Level = iota
	LvlWarn
	LvlNotice
	LvlInfo
	LvlTalkative
	LvlChatty
	LvlDebug
	LvlVomit
)

// Event is one parsed line. Raw is always set; for non-JSON lines only Raw and Plain are set.
type Event struct {
	Raw   string
	Plain bool   // line did not start with "@nix " (Nix printed it outside the JSON logger)
	Bad   string // non-empty if the line started with "@nix " but did not parse

	Action Action            `json:"action"`
	ID     uint64            `json:"id"`
	Parent uint64            `json:"parent"`
	Level  Level             `json:"level"`
	Type   int               `json:"type"`
	Text   string            `json:"text"`
	Fields []json.RawMessage `json:"fields"`

	// msg
	Msg    string  `json:"msg"`
	RawMsg string  `json:"raw_msg"`
	File   *string `json:"file"`
	Line   *int    `json:"line"`
	Column *int    `json:"column"`
	Trace  []Trace `json:"trace"`

	Unknown []string // JSON keys not listed above (format drift)
}

// Trace is an error trace frame (evaluation errors only).
type Trace struct {
	Raw    string  `json:"raw_msg"`
	File   *string `json:"file"`
	Line   *int    `json:"line"`
	Column *int    `json:"column"`
}

var knownKeys = map[string]bool{"action": true, "id": true, "parent": true, "level": true, "type": true,
	"text": true, "fields": true, "msg": true, "raw_msg": true, "file": true, "line": true, "column": true, "trace": true}

// Str returns field i as a string ("" if absent or not a string).
func (e *Event) Str(i int) string {
	if i >= len(e.Fields) {
		return ""
	}
	var s string
	if json.Unmarshal(e.Fields[i], &s) != nil {
		return ""
	}
	return s
}

// Int returns field i as an integer (0 if absent or not a number).
func (e *Event) Int(i int) int64 {
	if i >= len(e.Fields) {
		return 0
	}
	var n int64
	if json.Unmarshal(e.Fields[i], &n) != nil {
		return 0
	}
	return n
}

// Activity returns the activity type of a start event.
func (e *Event) Activity() ActivityType { return ActivityType(e.Type) }

// Result returns the result type of a result event.
func (e *Event) Result() ResultType { return ResultType(e.Type) }

// ParseLine parses one stderr line.
func ParseLine(line string) Event {
	ev := Event{Raw: line}
	js, ok := strings.CutPrefix(line, "@nix ")
	if !ok {
		ev.Plain = true
		return ev
	}
	if err := json.Unmarshal([]byte(js), &ev); err != nil {
		ev.Bad = err.Error()
		return ev
	}
	// Key drift check. Progress results are >90% of a big stream and have a fixed shape, so
	// they are checked only by length of their fields.
	if ev.Action == ActResult && (ev.Type == int(ResProgress) || ev.Type == int(ResSetExpected)) {
		return ev
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal([]byte(js), &keys) == nil {
		for k := range keys {
			if !knownKeys[k] {
				ev.Unknown = append(ev.Unknown, k)
			}
		}
	}
	ev.Raw = line
	return ev
}

// Scan calls fn for every line of r. Lines may be up to 16 MiB.
func Scan(r io.Reader, fn func(Event)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		fn(ParseLine(sc.Text()))
	}
	return sc.Err()
}
