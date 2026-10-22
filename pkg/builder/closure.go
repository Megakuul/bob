package builder

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// PathInfo is one store path of an exportReferencesGraph closure.
type PathInfo struct {
	Path       string   `json:"path"`
	References []string `json:"references"`
}

// ReferencesGraph returns the closure Nix wrote for the structured attribute
// exportReferencesGraph.<attr> (a list of store paths and their references), sorted by path.
// Bundle kinds (appimage, oci, tarball) read the runtime closure of their roots from it.
func (c *Ctx) ReferencesGraph(attr string) ([]PathInfo, error) {
	f, err := os.Open(os.Getenv("NIX_ATTRS_JSON_FILE"))
	if err != nil {
		return nil, Invalidf("%v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxAttrs+1))
	if err != nil {
		return nil, Invalidf("reading the attributes: %v", err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, Invalidf("attributes: %v", err)
	}
	raw, ok := all[attr]
	if !ok {
		return nil, Invalidf("attribute %s (exportReferencesGraph) is missing", attr)
	}
	var infos []PathInfo
	if err := json.Unmarshal(raw, &infos); err != nil {
		return nil, Invalidf("attribute %s: %v", attr, err)
	}
	for _, in := range infos {
		if !strings.HasPrefix(in.Path, c.StoreDir+"/") || strings.ContainsAny(in.Path[len(c.StoreDir)+1:], "/\x00") {
			return nil, Invalidf("attribute %s: %q is not a store path", attr, in.Path)
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos, nil
}

// CodedDiag writes a diagnostic that carries a bob error code and its message arguments
// (`bob:: {"type":"diag","severity":…,"code":2015,"args":{…},"msg":…}`): the translator reports it
// with that code instead of a generic build failure (an error), or as a warning of a successful
// build (severity "warning").
func (c *Ctx) CodedDiag(severity string, code int, args map[string]string, msg string) {
	b, _ := json.Marshal(struct {
		Type     string            `json:"type"`
		Severity string            `json:"severity"`
		Code     int               `json:"code"`
		Args     map[string]string `json:"args"`
		Msg      string            `json:"msg"`
	}{"diag", severity, code, args, msg})
	fmt.Fprintf(c.stderr, "bob:: %s\n", b)
}
