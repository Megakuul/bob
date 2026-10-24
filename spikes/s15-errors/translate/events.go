package translate

// SchemaVersion of the --json event stream [proposal].
const SchemaVersion = 1

// Event is one line of `bob build --json` (NDJSON on stdout). Field names are stable within a
// schema version; new fields may be added, never removed or renamed.
type Event struct {
	V      int    `json:"v"`
	Event  string `json:"event"` // run.start | package.start | package.wait | upstream.start | log | package.finish | error | run.finish
	Time   string `json:"time,omitempty"`
	Label  string `json:"label,omitempty"`
	Action string `json:"action,omitempty"` // package.start: build | download
	Cache  string `json:"cache,omitempty"`
	Name   string `json:"name,omitempty"` // upstream.start: nixpkgs package name
	Reason string `json:"reason,omitempty"`
	Line   string `json:"line,omitempty"` // log (only with --json-log)
	Status Status `json:"status,omitempty"`
	Error  *Error `json:"error,omitempty"`

	Requested []string `json:"requested,omitempty"` // run.start
	ExitCode  *int     `json:"exitCode,omitempty"`  // run.finish
	Counts    *Counts  `json:"counts,omitempty"`    // run.finish
	Drift     []string `json:"drift,omitempty"`     // run.finish
}
