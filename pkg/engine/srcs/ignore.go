package srcs

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"strings"
)

// gitignore semantics, implemented in Go (R-FW-021; no git binary). A rule set belongs to the
// directory of its file (base, repo-relative, "" for the root). Precedence, lowest first:
// .git/info/exclude, then the .gitignore files from the root down; within one file the last
// matching rule wins. A directory that is ignored is not entered, so nothing below it can be
// re-included (as in git).

type rule struct {
	base    string   // directory of the rule's file, repo-relative ("" = root)
	segs    []string // pattern split at "/"
	neg     bool     // "!pattern"
	dirOnly bool     // "pattern/"
}

// parseIgnore parses the rules of one gitignore-format file whose directory is base.
func parseIgnore(data []byte, base string) []rule {
	var rules []rule
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = trimTrailingSpace(line)
		r := rule{base: base}
		if strings.HasPrefix(line, "!") {
			r.neg = true
			line = line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		// A slash at the start or in the middle anchors the pattern to base; otherwise it
		// matches at any depth below base.
		if strings.Contains(line, "/") {
			line = strings.TrimPrefix(line, "/")
		} else {
			line = "**/" + line
		}
		r.segs = strings.Split(line, "/")
		rules = append(rules, r)
	}
	return rules
}

// trimTrailingSpace removes trailing spaces unless they are escaped with a backslash.
func trimTrailingSpace(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, `\ `) {
		s = s[:len(s)-1]
	}
	return s
}

// ignored reports whether the repo-relative path rel (a directory when isDir) is ignored by
// rules, given in precedence order (lowest first).
func ignored(rules []rule, rel string, isDir bool) bool {
	ign := false
	for _, r := range rules {
		if r.dirOnly && !isDir {
			continue
		}
		sub := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			sub = rel[len(r.base)+1:]
		}
		if matchSegs(r.segs, strings.Split(sub, "/")) {
			ign = !r.neg
		}
	}
	return ign
}

// matchSegs matches path segments against pattern segments: "**" matches zero or more
// segments, other segments match one segment with path.Match ("*", "?", "[…]", "\" escapes).
func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return len(name) > 0
			}
			for i := 0; i <= len(name); i++ {
				if matchSegs(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// readIgnore reads a gitignore-format file; a missing file has no rules.
func readIgnore(file, base string) ([]rule, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseIgnore(b, base), nil
}
