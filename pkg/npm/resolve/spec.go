package resolve

import (
	"fmt"
	"regexp"
	"strings"
)

// specType is the npm-package-arg type of a registry dependency spec.
type specType int

const (
	specRange specType = iota
	specVersion
	specTag
)

// spec is a parsed dependency spec as far as bob supports them: registry ranges, exact
// versions, dist-tags and npm: aliases of those. git, file, directory and URL specs are
// rejected in v1 (SPEC §03.4.1).
type spec struct {
	name  string // registry package name (the alias target for npm: specs)
	alias bool
	typ   specType
	fetch string // the range, version or tag (trimmed)
}

var (
	reURLSpec  = regexp.MustCompile(`(?i)^(?:git[+])?[a-z]+:`)
	reFileType = regexp.MustCompile(`(?i)[.](?:tgz|tar\.gz|tar)$`)
	reFileSpec = regexp.MustCompile(`^(?:[.]|~[/]|[/]|[a-zA-Z]:)`)
	reGitSpec  = regexp.MustCompile(`(?i)^[^@]+@[^:.]+\.[^:]+:.+$`)
)

// ValidName reports whether name is an npm package name bob accepts: npm's rules for old
// packages (validate-npm-package-name validForOldPackages), at most 214 characters, and no
// path segment that is "." or "..". Names become directory names, so this is a security check.
func ValidName(name string) bool {
	if name == "" || len(name) > 214 || strings.TrimSpace(name) != name {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "_") {
		return false
	}
	if l := strings.ToLower(name); l == "node_modules" || l == "favicon.ico" {
		return false
	}
	scope, pkg := "", name
	if strings.HasPrefix(name, "@") {
		i := strings.IndexByte(name, '/')
		if i < 0 {
			return false
		}
		scope, pkg = name[1:i], name[i+1:]
		if scope == "" || scope == "." || scope == ".." || !uriSafe(scope) {
			return false
		}
	}
	if pkg == "" || pkg == "." || pkg == ".." || strings.HasPrefix(pkg, ".") || !uriSafe(pkg) {
		return false
	}
	return true
}

// uriSafe is encodeURIComponent(s) === s.
func uriSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-_.!~*'()", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// parseSpec is npa.resolve(name, raw) restricted to registry specs.
func parseSpec(name, raw string) (spec, error) {
	if !ValidName(name) {
		return spec{}, fmt.Errorf("invalid package name %q", name)
	}
	if strings.HasPrefix(strings.ToLower(raw), "npm:") {
		rest := raw[4:]
		sub, subRaw := rest, "*"
		if i := strings.IndexByte(rest[min(1, len(rest)):], '@'); i >= 0 {
			i++ // index into rest (the search skipped a leading "@")
			sub, subRaw = rest[:i], rest[i+1:]
			if subRaw == "" {
				subRaw = "*"
			}
		}
		if strings.HasPrefix(strings.ToLower(subRaw), "npm:") {
			return spec{}, fmt.Errorf("%s: nested aliases are not supported", name)
		}
		s, err := parseSpec(sub, subRaw)
		if err != nil {
			return spec{}, fmt.Errorf("%s: alias %q: %w", name, raw, err)
		}
		s.alias = true
		return s, nil
	}
	if raw == "" {
		raw = "*"
	}
	switch {
	case reFileSpec.MatchString(raw) || strings.HasPrefix(strings.ToLower(raw), "file:"):
		return spec{}, fmt.Errorf("%s@%s: file and directory dependencies are not supported", name, raw)
	case reURLSpec.MatchString(raw) || reGitSpec.MatchString(raw):
		return spec{}, fmt.Errorf("%s@%s: git and URL dependencies are not supported", name, raw)
	case strings.ContainsAny(raw, `/\`) || reFileType.MatchString(raw):
		return spec{}, fmt.Errorf("%s@%s: git, file and URL dependencies are not supported", name, raw)
	}
	s := spec{name: name, fetch: strings.TrimSpace(raw)}
	if ValidVersion(s.fetch) {
		s.typ = specVersion
		return s, nil
	}
	if _, err := ParseRange(s.fetch); err == nil {
		s.typ = specRange
		return s, nil
	}
	if !uriSafe(s.fetch) || s.fetch == "" {
		return spec{}, fmt.Errorf("%s: invalid version range or tag %q", name, raw)
	}
	s.typ = specTag
	return s, nil
}
