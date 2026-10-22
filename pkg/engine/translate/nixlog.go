package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// The parser of Nix's `--log-format internal-json` stream: `@nix {json}` lines on stderr
// (S15 §3.1). It knows every action, activity type and result type that Nix 2.34 emits and
// reports anything else as drift.

type action string

const (
	actMsg    action = "msg"
	actStart  action = "start"
	actStop   action = "stop"
	actResult action = "result"
)

// activityType mirrors Nix's ActivityType enum.
type activityType int

const (
	atUnknown       activityType = 0
	atCopyPath      activityType = 100
	atFileTransfer  activityType = 101
	atRealise       activityType = 102
	atCopyPaths     activityType = 103
	atBuilds        activityType = 104
	atBuild         activityType = 105
	atOptimiseStore activityType = 106
	atVerifyPaths   activityType = 107
	atSubstitute    activityType = 108
	atQueryPathInfo activityType = 109
	atPostBuildHook activityType = 110
	atBuildWaiting  activityType = 111
	atFetchTree     activityType = 112
)

func (t activityType) known() bool { return t == atUnknown || (t >= atCopyPath && t <= atFetchTree) }

// resultType mirrors Nix's ResultType enum.
type resultType int

const (
	rtFileLinked       resultType = 100
	rtBuildLogLine     resultType = 101
	rtUntrustedPath    resultType = 102
	rtCorruptedPath    resultType = 103
	rtSetPhase         resultType = 104
	rtProgress         resultType = 105
	rtSetExpected      resultType = 106
	rtPostBuildLogLine resultType = 107
	rtFetchStatus      resultType = 108
)

func (t resultType) known() bool { return t >= rtFileLinked && t <= rtFetchStatus }

// Nix verbosity levels that matter to the translator.
const (
	lvlError = 0
	lvlWarn  = 1
)

// event is one parsed stderr line.
type event struct {
	raw   string
	plain bool   // the line did not start with "@nix " (Nix printed it outside the JSON logger)
	bad   string // the line started with "@nix " but did not parse

	Action action            `json:"action"`
	ID     uint64            `json:"id"`
	Parent uint64            `json:"parent"`
	Level  int               `json:"level"`
	Type   int               `json:"type"`
	Text   string            `json:"text"`
	Fields []json.RawMessage `json:"fields"`
	Msg    string            `json:"msg"`
	RawMsg string            `json:"raw_msg"`
	File   *string           `json:"file"`
	Line   *int              `json:"line"`
	Column *int              `json:"column"`
	Trace  []json.RawMessage `json:"trace"`

	unknown []string // JSON keys not listed above (format drift)
	hasRaw  bool     // the line has a raw_msg key: Nix's own error logger wrote it (F7)
}

func knownKey(k string) bool {
	switch k {
	case "action", "id", "parent", "level", "type", "text", "fields", "msg", "raw_msg", "file", "line", "column", "trace":
		return true
	}
	return false
}

// str returns field i as a string ("" if absent or not a string).
func (e *event) str(i int) string {
	if i >= len(e.Fields) {
		return ""
	}
	var s string
	if json.Unmarshal(e.Fields[i], &s) != nil {
		return ""
	}
	return s
}

// num returns field i as an integer (0 if absent or not a number).
func (e *event) num(i int) int64 {
	if i >= len(e.Fields) {
		return 0
	}
	var n int64
	if json.Unmarshal(e.Fields[i], &n) != nil {
		return 0
	}
	return n
}

// parseLine parses one stderr line (without its newline).
func parseLine(line []byte) event {
	ev := event{raw: string(line)}
	js, ok := bytes.CutPrefix(line, []byte("@nix "))
	if !ok {
		ev.plain = true
		return ev
	}
	if err := json.Unmarshal(js, &ev); err != nil {
		ev.bad = err.Error()
		return ev
	}
	// Progress results are about 90 % of a big stream and have a fixed shape; the key check
	// is skipped for them (S15 §3.4).
	if ev.Action == actResult && (ev.Type == int(rtProgress) || ev.Type == int(rtSetExpected)) {
		return ev
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(js, &keys) == nil {
		_, ev.hasRaw = keys["raw_msg"]
		for k := range keys {
			if !knownKey(k) {
				ev.unknown = append(ev.unknown, k)
			}
		}
		sort.Strings(ev.unknown)
	}
	return ev
}

// MaxLineBytes bounds one stderr line. Longer lines are cut (hostile input, AGENTS.md §5.4);
// the cut is reported as drift.
const MaxLineBytes = 1 << 20

// errLineTooLong marks a line cut at MaxLineBytes.
var errLineTooLong = errors.New("line longer than the limit")

// scanLines calls fn for every line of r, each at most MaxLineBytes long. A longer line is cut
// and fn is called with its first MaxLineBytes bytes and errLineTooLong.
func scanLines(r io.Reader, fn func(line []byte, err error)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case len(buf)+len(chunk) <= MaxLineBytes:
			buf = append(buf, chunk...)
		case !cut:
			buf = append(buf, chunk[:MaxLineBytes-len(buf)]...)
			cut = true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(buf) > 0 || err == nil {
			line := bytes.TrimSuffix(buf, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			var lerr error
			if cut {
				lerr = errLineTooLong
			}
			fn(line, lerr)
		}
		buf, cut = buf[:0], false
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("translate: read Nix log: %w", err)
		}
	}
}

// stripANSI removes terminal escape sequences (compilers and Nix colour their output).
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}
