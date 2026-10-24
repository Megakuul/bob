// Package licence checks the licences of bob's Go and npm dependencies
// against an allowlist (HARNESS.md §1.3 stage 7, AGENTS.md §5.7).
//
// Detection is done here, offline, from the licence files in the module
// cache and the licence fields of web/package-lock.json. bob is AGPL-3.0, so
// the allowlist holds only permissive and weak file-level copyleft licences;
// GPL, AGPL and LGPL are never on it and pass only as an exception that names
// an owner decision (D<n>).
package licence

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Unknown is the licence of a dependency whose licence text matched nothing.
const Unknown = "UNKNOWN"

// Policy is the parsed allowlist file (tools/ci/licences.allow).
type Policy struct {
	Allowed    map[string]bool
	Exceptions map[string]Exception // key: subject + " " + licence
}

// Exception admits one licence for one dependency, backed by an owner
// decision reference.
type Exception struct {
	Subject  string // Go module path or "npm:<name>"
	Licence  string
	Approval string // owner decision, e.g. D52
}

// copyleft are the licence families that may never be allowlisted wholesale.
var copyleft = regexp.MustCompile(`^(A?GPL|LGPL)-`)

var approvalRef = regexp.MustCompile(`^D[0-9]+$`)

// ParsePolicy reads the allowlist. Lines are
//
//	allow <SPDX>
//	except <subject> <SPDX> <approval-ref> [note…]
//
// and '#' starts a comment.
func ParsePolicy(r io.Reader) (*Policy, error) {
	p := &Policy{Allowed: map[string]bool{}, Exceptions: map[string]Exception{}}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "allow" && len(f) == 2:
			if copyleft.MatchString(f[1]) {
				return nil, fmt.Errorf("line %d: %s cannot be allowed wholesale; add an exception with an owner approval", n, f[1])
			}
			p.Allowed[f[1]] = true
		case f[0] == "except" && len(f) >= 4:
			if !approvalRef.MatchString(f[3]) {
				return nil, fmt.Errorf("line %d: exception for %s needs an owner approval reference (D<n>), got %q", n, f[1], f[3])
			}
			e := Exception{Subject: f[1], Licence: f[2], Approval: f[3]}
			p.Exceptions[e.Subject+" "+e.Licence] = e
		default:
			return nil, fmt.Errorf("line %d: cannot parse %q", n, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	return p, nil
}

// LoadPolicy parses the allowlist file at path.
func LoadPolicy(path string) (*Policy, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open policy: %w", err)
	}
	defer f.Close()
	return ParsePolicy(f)
}

// Finding is one dependency and the verdict on it.
type Finding struct {
	Subject  string   // module path or npm:<name>
	Version  string   //
	Licences []string // detected licences (SPDX ids)
	OK       bool
	Reason   string // why it failed, or the approval reference that let it pass
}

// judge decides a dependency. All detected licences must pass (a module with
// LICENSE and COPYING files is bound by both), except that an SPDX "OR"
// expression is handled before this by the npm path.
func (p *Policy) judge(subject string, lics []string) (bool, string) {
	if len(lics) == 0 {
		lics = []string{Unknown}
	}
	var refs, bad []string
	for _, l := range lics {
		if p.Allowed[l] {
			continue
		}
		if e, ok := p.Exceptions[subject+" "+l]; ok {
			refs = append(refs, e.Approval)
			continue
		}
		bad = append(bad, l)
	}
	if len(bad) > 0 {
		return false, "not allowed: " + strings.Join(bad, ", ")
	}
	return true, strings.Join(refs, ",")
}

// signatures map licence text to SPDX ids. Order matters: the MPL-2.0 text
// names the GPL family as "secondary licenses", and the LGPL and AGPL texts
// mention the GPL, so those are tested first.
var signatures = []struct {
	id  string
	all []string
}{
	{"MPL-2.0", []string{"mozilla public license", "2.0"}},
	{"AGPL-3.0", []string{"gnu affero general public license"}},
	{"LGPL-3.0", []string{"gnu lesser general public license", "version 3"}},
	{"LGPL-2.1", []string{"gnu lesser general public license"}},
	{"LGPL-2.0", []string{"gnu library general public license"}},
	{"GPL-3.0", []string{"gnu general public license", "version 3"}},
	{"GPL-2.0", []string{"gnu general public license"}},
	{"Apache-2.0", []string{"apache license", "version 2.0"}},
	{"Unlicense", []string{"this is free and unencumbered software released into the public domain"}},
	{"CC0-1.0", []string{"cc0 1.0 universal"}},
	{"Zlib", []string{"provided 'as-is'", "altered source versions must be plainly marked"}},
	{"ISC", []string{"permission to use, copy, modify, and", "distribute this software for any purpose with or without fee"}},
	{"MIT", []string{"permission is hereby granted, free of charge"}},
	{"BSD-3-Clause", []string{"redistribution and use in source and binary forms", "endorse or promote products"}},
	{"BSD-2-Clause", []string{"redistribution and use in source and binary forms"}},
}

var spaces = regexp.MustCompile(`\s+`)

// Classify returns the SPDX id of a licence text, or Unknown.
func Classify(text string) string {
	t := spaces.ReplaceAllString(strings.ToLower(text), " ")
	for _, s := range signatures {
		ok := true
		for _, needle := range s.all {
			if !strings.Contains(t, needle) {
				ok = false
				break
			}
		}
		if ok {
			return s.id
		}
	}
	return Unknown
}

var licenceFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|unlicense)([-._].*)?$`)

// DirLicences classifies every licence file at the top of dir.
func DirLicences(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read module dir: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		// LICENSE.docs (e.g. CC-BY-SA for the documentation of
		// opencontainers/go-digest) covers prose, not the code bob links.
		if e.IsDir() || !licenceFile.MatchString(e.Name()) || strings.HasSuffix(strings.ToLower(e.Name()), ".docs") || strings.HasSuffix(strings.ToLower(e.Name()), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read licence file: %w", err)
		}
		if id := Classify(string(b)); !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

type goModule struct {
	Path    string
	Version string
	Main    bool
	Dir     string
	Replace *goModule
}

// CheckGoModule checks every dependency in the build list of the Go module
// at dir. The workspace is switched off so each module is judged on its own
// graph (tool dependencies stay in the tools module).
func CheckGoModule(ctx context.Context, dir string, p *Policy) ([]Finding, error) {
	out, err := goJSON(ctx, dir, "list", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	var findings []Finding
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var m goModule
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list: %w", err)
		}
		if m.Main {
			continue
		}
		mdir := m.Dir
		if m.Replace != nil && m.Replace.Dir != "" {
			mdir = m.Replace.Dir
		}
		if mdir == "" {
			// Not in the module cache yet; download fills it.
			d, err := goJSON(ctx, dir, "mod", "download", "-json", m.Path+"@"+m.Version)
			if err != nil {
				return nil, err
			}
			var dm goModule
			if err := json.Unmarshal([]byte(d), &dm); err != nil {
				return nil, fmt.Errorf("decode go mod download: %w", err)
			}
			mdir = dm.Dir
		}
		lics, err := DirLicences(mdir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Path, err)
		}
		ok, reason := p.judge(m.Path, lics)
		findings = append(findings, Finding{Subject: m.Path, Version: m.Version, Licences: lics, OK: ok, Reason: reason})
	}
	return findings, nil
}

func goJSON(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s in %s: %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

type npmLock struct {
	Packages map[string]struct {
		Version string          `json:"version"`
		License json.RawMessage `json:"license"`
		Link    bool            `json:"link"`
	} `json:"packages"`
}

// CheckNPMLock checks the licence field of every package in a
// package-lock.json (lockfileVersion 2 or 3).
func CheckNPMLock(path string, p *Policy) ([]Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	var lock npmLock
	if err := json.Unmarshal(b, &lock); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var findings []Finding
	for key, pkg := range lock.Packages {
		if key == "" || pkg.Link {
			continue // the root package and workspace links are bob's own code
		}
		name := key[strings.LastIndex(key, "node_modules/")+len("node_modules/"):]
		subject := "npm:" + name
		var expr string
		if len(pkg.License) > 0 && json.Unmarshal(pkg.License, &expr) != nil {
			expr = "" // legacy object form: treat as unknown
		}
		lics, ok, reason := p.judgeSPDX(subject, expr)
		findings = append(findings, Finding{Subject: subject, Version: pkg.Version, Licences: lics, OK: ok, Reason: reason})
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Subject < findings[j].Subject })
	return findings, nil
}

// judgeSPDX accepts an SPDX expression when any OR alternative passes in
// full. Parentheses are flattened; npm metadata rarely nests deeper.
func (p *Policy) judgeSPDX(subject, expr string) ([]string, bool, string) {
	expr = strings.NewReplacer("(", " ", ")", " ").Replace(expr)
	alts := strings.Split(expr, " OR ")
	var last string
	var lastLics []string
	for _, alt := range alts {
		var lics []string
		for _, l := range strings.Split(alt, " AND ") {
			if l = strings.TrimSpace(l); l != "" {
				lics = append(lics, l)
			}
		}
		ok, reason := p.judge(subject, lics)
		if ok {
			return lics, true, reason
		}
		last, lastLics = reason, lics
	}
	if len(lastLics) == 0 {
		lastLics = []string{Unknown}
	}
	return lastLics, false, last
}

// CheckAttribution reports every file under the asset directories that has
// no row in ATTRIBUTION.md. A row is any mention of the file path or of one
// of its parent directories followed by '/'.
func CheckAttribution(root string, dirs []string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "ATTRIBUTION.md"))
	if err != nil {
		return nil, fmt.Errorf("read ATTRIBUTION.md: %w", err)
	}
	attr := string(b)
	var missing []string
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(path string, e os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			if err != nil || e.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			for p := rel; p != "." && p != "/"; p = filepath.ToSlash(filepath.Dir(p)) {
				if strings.Contains(attr, "`"+p+"`") || (p != rel && strings.Contains(attr, "`"+p+"/`")) {
					return nil
				}
			}
			missing = append(missing, rel)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", d, err)
		}
	}
	return missing, nil
}
