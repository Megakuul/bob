package lock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Version is the lockVersion bob writes and reads.
const Version = 1

// File is bob.lock in memory. Sections are raw JSON owned by their ecosystem (SPEC §1.8).
type File struct {
	LockVersion int
	Nixpkgs     NixpkgsPin
	Sources     map[string]Source
	Sections    map[string]json.RawMessage // "go", "rust", "npm", "opam", "cc" (and "nixpkgsFacts")
}

// NixpkgsPin is the "nixpkgs" entry (R-NIX-005).
type NixpkgsPin struct {
	Channel string `json:"channel"`
	Inputs  string `json:"inputs,omitempty"` // sha256 hex of RepoOpts.Nixpkgs (R-FW-062)
	NarHash string `json:"narHash"`
	Rev     string `json:"rev"`
	URL     string `json:"url"`
}

// Source is one "sources" entry (bob.Fetch). The entry is its own declaration (url, kind, strip,
// name), so freshness is decided per entry.
type Source struct {
	Hash    string `json:"hash"`
	Kind    string `json:"kind"` // file | archive
	Name    string `json:"name"`
	NarHash string `json:"narHash,omitempty"`
	Strip   int    `json:"strip"`
	URL     string `json:"url"`
}

// Read reads and validates bob.lock. A missing file is returned as the os error (errors.Is
// fs.ErrNotExist); unreadable content is BOB-E-1019 (R-FW-048).
func Read(path string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(fh, maxLockBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lock: reading %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates the bytes of a bob.lock (hostile input: size-capped, every entry
// validated). Errors are *diag.Error BOB-E-1019 with the line of the problem.
func Parse(data []byte) (*File, error) {
	if len(data) > maxLockBytes {
		return nil, unreadable(fmt.Sprintf("larger than %d MiB", maxLockBytes>>20), lineAt(data, maxLockBytes))
	}
	if n, marker := conflictMarker(data); n > 0 {
		return nil, unreadable("merge conflict marker "+marker, n)
	}
	if !utf8.Valid(data) {
		bad := 0
		for bad < len(data) {
			r, size := utf8.DecodeRune(data[bad:])
			if r == utf8.RuneError && size <= 1 {
				break
			}
			bad += size
		}
		return nil, unreadable("invalid UTF-8", lineAt(data, bad))
	}
	members, err := topLevel(data)
	if err != nil {
		return nil, err
	}
	f := &File{Sections: map[string]json.RawMessage{}}
	seenVersion := false
	for _, m := range members {
		bad := func(err error) error { return unreadable(err.Error(), m.line) }
		switch m.key {
		case "lockVersion":
			n, err := strconv.Atoi(string(bytes.TrimSpace(m.raw)))
			if err != nil || n != Version {
				return nil, unreadable(fmt.Sprintf("lockVersion %s is not supported (this bob reads %d)", clip(string(m.raw)), Version), m.line)
			}
			f.LockVersion, seenVersion = n, true
		case "nixpkgs":
			if err := strictDecode(m.raw, &f.Nixpkgs); err != nil {
				return nil, bad(fmt.Errorf("nixpkgs: %w", err))
			}
			if err := f.Nixpkgs.validate(); err != nil {
				return nil, bad(err)
			}
		case "sources":
			if err := strictDecode(m.raw, &f.Sources); err != nil {
				return nil, bad(fmt.Errorf("sources: %w", err))
			}
			if err := validateSources(f.Sources); err != nil {
				return nil, bad(err)
			}
		default:
			if !slices.Contains(sectionKeys, m.key) {
				return nil, unreadable(fmt.Sprintf("unknown member %q", clip(m.key)), m.line)
			}
			if _, err := DecodeSection(m.key, m.raw); err != nil {
				return nil, bad(err)
			}
			f.Sections[m.key] = m.raw
		}
	}
	if !seenVersion {
		return nil, unreadable("lockVersion is missing", 1)
	}
	return f, nil
}

// member is one top-level member of bob.lock.
type member struct {
	key  string
	raw  json.RawMessage
	line int
}

// topLevel splits the document into its members, with line numbers for errors.
func topLevel(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	syntax := func(err error) error {
		off := int(dec.InputOffset())
		var se *json.SyntaxError
		if errors.As(err, &se) {
			// Decode reports the offset after the bad byte, Token the offset before it: step
			// back one byte, then over whitespace to the byte that is wrong.
			off = max(0, int(se.Offset)-1)
			for off < len(data) && strings.IndexByte(" \t\r\n", data[off]) >= 0 {
				off++
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return unreadable("unexpected end of file", lineAt(data, len(data)))
		}
		return unreadable(err.Error(), lineAt(data, off))
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, syntax(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, unreadable("the top level is not a JSON object", lineAt(data, int(dec.InputOffset())))
	}
	var out []member
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, syntax(err)
		}
		key, _ := tok.(string)
		line := lineAt(data, int(dec.InputOffset()))
		if seen[key] {
			return nil, unreadable(fmt.Sprintf("member %q appears twice", clip(key)), line)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, syntax(err)
		}
		out = append(out, member{key: key, raw: raw, line: line})
	}
	if _, err := dec.Token(); err != nil {
		return nil, syntax(err)
	}
	if err := atEOF(dec); err != nil {
		return nil, unreadable("unexpected data after the closing brace", lineAt(data, int(dec.InputOffset())))
	}
	return out, nil
}

// unreadable is BOB-E-1019.
func unreadable(reason string, line int) *diag.Error {
	e := diag.New(1019, "reason", reason, "line", strconv.Itoa(line))
	e.DefinedAt = diag.Pos{File: "bob.lock", Line: line}
	return e
}

// lineAt returns the 1-based line of byte offset off.
func lineAt(data []byte, off int) int {
	off = max(0, min(off, len(data)))
	return bytes.Count(data[:off], []byte("\n")) + 1
}

var markerRe = regexp.MustCompile(`(?m)^(<{7}|={7}|>{7}|\|{7})(?:[ \t].*)?\r?$`)

// conflictMarker returns the line (1-based) and text of the first merge conflict marker.
func conflictMarker(data []byte) (int, string) {
	loc := markerRe.FindSubmatchIndex(data)
	if loc == nil {
		return 0, ""
	}
	return lineAt(data, loc[0]), string(data[loc[2]:loc[3]])
}

// Stale reports whether section key is missing or was resolved from other declarations than
// inputs (the section's "inputs", SPEC R-FW-062).
func (f *File) Stale(key, inputs string) bool {
	raw, ok := f.Sections[key]
	if !ok {
		return true
	}
	var s struct {
		Inputs string `json:"inputs"`
	}
	return json.Unmarshal(raw, &s) != nil || s.Inputs != inputs
}

var revRe = regexp.MustCompile(`"rev"\s*:\s*"([0-9a-f]{40})"`)

// SalvageNixpkgsRev returns the first nixpkgs revision found in a bob.lock that cannot be
// parsed (bob lock --rebuild keeps nixpkgs.rev, R-FW-048), or "".
func SalvageNixpkgsRev(data []byte) string {
	if m := revRe.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// Encode returns the canonical bytes of f (R-FW-004): 2-space indent, keys sorted, one final
// newline. Invalid content is an error, so bob never writes a lock it cannot read back.
func (f *File) Encode() ([]byte, error) {
	if f.LockVersion != Version {
		return nil, fmt.Errorf("lock: lockVersion %d is not supported", f.LockVersion)
	}
	top := map[string]any{"lockVersion": f.LockVersion}
	if f.Nixpkgs != (NixpkgsPin{}) {
		if err := f.Nixpkgs.validate(); err != nil {
			return nil, fmt.Errorf("lock: %w", err)
		}
		top["nixpkgs"] = f.Nixpkgs
	}
	if len(f.Sources) > 0 {
		if err := validateSources(f.Sources); err != nil {
			return nil, fmt.Errorf("lock: %w", err)
		}
		top["sources"] = f.Sources
	}
	for k, raw := range f.Sections {
		if _, err := DecodeSection(k, raw); err != nil {
			return nil, unpinnable(k, err)
		}
		top[k] = raw
	}
	b, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	b, err = canonical(b, "  ")
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	return b, nil
}

// Write writes f canonically and atomically (temporary file + rename), and only if the bytes
// change.
func (f *File) Write(path string) error {
	data, err := f.Encode()
	if err != nil {
		return err
	}
	_, err = WriteBytes(path, data)
	return err
}

// WriteBytes writes data to path atomically unless the file already holds exactly data; it
// reports whether it wrote.
func WriteBytes(path string, data []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("lock: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return false, fmt.Errorf("lock: writing %s: %w", path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return false, fmt.Errorf("lock: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return false, fmt.Errorf("lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("lock: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		done = true
		return false, fmt.Errorf("lock: %w", err)
	}
	done = true
	return true, nil
}

func (p NixpkgsPin) validate() error {
	if !channelRe.MatchString(p.Channel) {
		return fmt.Errorf("nixpkgs: channel %q is invalid", clip(p.Channel))
	}
	if !hex40Re.MatchString(p.Rev) {
		return fmt.Errorf("nixpkgs: rev %q is not 40 hex digits", clip(p.Rev))
	}
	if err := checkHTTPURL("nixpkgs url", p.URL, ""); err != nil {
		return err
	}
	if err := checkSRI("nixpkgs narHash", p.NarHash, "sha256"); err != nil {
		return err
	}
	if p.Inputs != "" {
		return checkInputs(p.Inputs)
	}
	return nil
}

func validateSources(srcs map[string]Source) error {
	for id, s := range srcs {
		if err := checkText("source id", id); err != nil {
			return err
		}
		if err := checkHTTPURL("source "+id+" url", s.URL, ""); err != nil {
			return err
		}
		if err := checkSRI("source "+id+" hash", s.Hash, "sha256", "sha512"); err != nil {
			return err
		}
		if len(s.Name) > 211 || !storeName.MatchString(s.Name) {
			return fmt.Errorf("source %s: store name %q is invalid", id, clip(s.Name))
		}
		if s.Strip < 0 || s.Strip > maxStrip {
			return fmt.Errorf("source %s: strip %d is out of range 0..%d", id, s.Strip, maxStrip)
		}
		switch s.Kind {
		case "archive":
			if err := checkSRI("source "+id+" narHash", s.NarHash, "sha256"); err != nil {
				return err
			}
		case "file":
			if s.NarHash != "" || s.Strip != 0 {
				return fmt.Errorf("source %s: a file source has no narHash or strip", id)
			}
		default:
			return fmt.Errorf("source %s: kind %q is not file or archive", id, clip(s.Kind))
		}
	}
	return nil
}
