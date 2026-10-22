// Package ref is the reference model of bob's RBAC (SPEC §6.4.1) used by the rbac and auth
// suites. It is test code: it reads the method rules from proto/**/*.proto with a small
// text scanner (the repository has no protobuf runtime dependency in the root module yet, so
// protoregistry is not available) and evaluates permissions without touching the server's own
// code, so the server is always compared against an independent implementation.
package ref

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind is bob.authz.v1.Kind without the KIND_ prefix.
type Kind string

// The rule kinds of SPEC §6.4.
const (
	KindUnspecified   Kind = "UNSPECIFIED"
	KindProject       Kind = "PROJECT"
	KindGlobal        Kind = "GLOBAL"
	KindListFiltered  Kind = "LIST_FILTERED"
	KindAuthenticated Kind = "AUTHENTICATED"
	KindPublic        Kind = "PUBLIC"
	KindWorker        Kind = "WORKER"
)

// Method is one rpc with its authorization rule.
type Method struct {
	Package string // bob.build.v1
	Service string // BuildService
	Name    string // CancelBuild
	Request string // CancelBuildRequest
	Stream  bool   // server or client streaming
	Kind    Kind   // UNSPECIFIED when the method has no rule
	HasRule bool   // an authz option is present at all

	ProjectField  string // KIND_PROJECT
	Resolve       string // RESOLVE_* without prefix; "" or UNSPECIFIED = project id
	AllowDownload bool   // allow_download_scope
	GetOK         bool   // idempotency_level = NO_SIDE_EFFECTS (Connect GET allowed)
	File          string // proto file, for messages
}

// Full is "<package>.<Service>/<Method>", the permission and URL form.
func (m Method) Full() string { return m.Package + "." + m.Service + "/" + m.Name }

// Path is the connect URL path below the server base.
func (m Method) Path() string { return "/api/" + m.Package + "." + m.Service + "/" + m.Name }

// Catalog is every method of a proto tree plus its message field index.
type Catalog struct {
	Methods []Method
	// fields["bob.build.v1.CancelBuildRequest"]["id"] = "string"
	fields map[string]map[string]string
}

var (
	reComment = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)
	reService = regexp.MustCompile(`\bservice\s+(\w+)\s*\{`)
	reMessage = regexp.MustCompile(`\bmessage\s+(\w+)\s*\{`)
	rePackage = regexp.MustCompile(`\bpackage\s+([\w.]+)\s*;`)
	reRPC     = regexp.MustCompile(`\brpc\s+(\w+)\s*\(\s*(stream\s+)?([\w.]+)\s*\)\s*returns\s*\(\s*(stream\s+)?([\w.]+)\s*\)`)
	reRule    = regexp.MustCompile(`(?s)option\s*\(\s*bob\.authz\.v1\.rule\s*\)\s*=\s*\{(.*?)\}`)
	reKV      = regexp.MustCompile(`(\w+)\s*:\s*("[^"]*"|[\w]+)`)
	reField   = regexp.MustCompile(`^\s*(repeated\s+)?(?:optional\s+)?([\w.]+)\s+(\w+)\s*=\s*\d+`)
)

// Load scans every .proto below root (the proto/ directory).
func Load(root string) (*Catalog, error) {
	c := &Catalog{fields: map[string]map[string]string{}}
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".proto") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if err := c.scan(f, string(b)); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
	}
	return c, nil
}

// block returns the text between the brace that opens at src[open] and its match.
func block(src string, open int) (string, int, error) {
	depth := 0
	inStr := false
	for i := open; i < len(src); i++ {
		switch ch := src[i]; {
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], i, nil
			}
		}
	}
	return "", 0, fmt.Errorf("unbalanced braces")
}

func (c *Catalog) scan(file, src string) error {
	src = reComment.ReplaceAllString(src, "")
	pkg := ""
	if m := rePackage.FindStringSubmatch(src); m != nil {
		pkg = m[1]
	}
	for _, loc := range reMessage.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		body, _, err := block(src, loc[1]-1)
		if err != nil {
			return err
		}
		fields := map[string]string{}
		depth := 0
		var line strings.Builder
		for _, ch := range body {
			switch ch {
			case '{':
				depth++
				if depth == 1 {
					line.Reset()
				}
			case '}':
				depth--
				line.Reset()
			case ';', '\n':
				if depth == 0 {
					if m := reField.FindStringSubmatch(line.String()); m != nil {
						fields[m[3]] = strings.TrimSpace(m[1] + m[2])
					}
					line.Reset()
				}
			default:
				if depth == 0 {
					line.WriteRune(ch)
				}
			}
		}
		c.fields[pkg+"."+name] = fields
	}
	for _, loc := range reService.FindAllStringSubmatchIndex(src, -1) {
		svc := src[loc[2]:loc[3]]
		body, _, err := block(src, loc[1]-1)
		if err != nil {
			return err
		}
		idx := reRPC.FindAllStringSubmatchIndex(body, -1)
		for i, r := range idx {
			end := len(body)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			text := body[r[0]:end]
			m := Method{
				Package: pkg, Service: svc, Name: body[r[2]:r[3]], Request: body[r[6]:r[7]],
				Stream: r[4] >= 0 || r[8] >= 0, Kind: KindUnspecified, File: file,
			}
			if strings.Contains(text, "idempotency_level = NO_SIDE_EFFECTS") {
				m.GetOK = true
			}
			if rm := reRule.FindStringSubmatch(text); rm != nil {
				m.HasRule = true
				for _, kv := range reKV.FindAllStringSubmatch(rm[1], -1) {
					v := strings.Trim(kv[2], `"`)
					switch kv[1] {
					case "kind":
						m.Kind = Kind(strings.TrimPrefix(v, "KIND_"))
					case "project_field":
						m.ProjectField = v
					case "resolve":
						m.Resolve = strings.TrimPrefix(v, "RESOLVE_")
					case "allow_download_scope":
						m.AllowDownload = v == "true"
					}
				}
			}
			c.Methods = append(c.Methods, m)
		}
	}
	return nil
}

// Field returns the declared type of a top-level field of a request message.
func (c *Catalog) Field(m Method, name string) (string, bool) {
	t, ok := c.fields[m.Package+"."+m.Request][name]
	return t, ok
}

// Find returns the method with the given full name.
func (c *Catalog) Find(full string) (Method, bool) {
	for _, m := range c.Methods {
		if m.Full() == full {
			return m, true
		}
	}
	return Method{}, false
}

// Problems lists every violation of R-AUTH-006: a method without a rule, with kind
// UNSPECIFIED, or a PROJECT rule whose project_field is not a top-level string field of the
// request. WORKER rules must sit on bob.worker.v1 only, and nothing else may live there.
func (c *Catalog) Problems() []string {
	var out []string
	for _, m := range c.Methods {
		switch {
		case !m.HasRule:
			out = append(out, m.Full()+": no bob.authz.v1.rule")
		case m.Kind == KindUnspecified:
			out = append(out, m.Full()+": rule kind is UNSPECIFIED")
		case m.Kind == KindProject:
			if m.ProjectField == "" {
				out = append(out, m.Full()+": PROJECT rule without project_field")
			} else if t, ok := c.Field(m, m.ProjectField); !ok || t != "string" {
				out = append(out, fmt.Sprintf("%s: project_field %q is not a top-level string field of %s", m.Full(), m.ProjectField, m.Request))
			}
		}
		if (m.Kind == KindWorker) != (m.Package == "bob.worker.v1") {
			out = append(out, m.Full()+": WORKER rules belong on bob.worker.v1 exactly")
		}
	}
	return out
}
