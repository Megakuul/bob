package resolve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// orderedMap is a JSON object of strings in JavaScript property order: integer-like keys first
// in ascending order, then the others in insertion order (a repeated key keeps its first
// position and takes the last value). npm iterates dependency objects in this order, and the
// order decides which peer sets are loaded first.
type orderedMap struct {
	keys []string
	vals map[string]string
}

func (m *orderedMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

func (m *orderedMap) Get(k string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m.vals[k]
	return v, ok
}

func (m *orderedMap) Keys() []string {
	if m == nil {
		return nil
	}
	return m.keys
}

func (m *orderedMap) Set(k, v string) {
	if m.vals == nil {
		m.vals = map[string]string{}
	}
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
		m.reorder()
	}
	m.vals[k] = v
}

func (m *orderedMap) Delete(k string) {
	if m == nil {
		return
	}
	if _, ok := m.vals[k]; !ok {
		return
	}
	delete(m.vals, k)
	for i, x := range m.keys {
		if x == k {
			m.keys = append(m.keys[:i:i], m.keys[i+1:]...)
			break
		}
	}
}

// arrayIndex reports whether k is a canonical array index ("0", "17"; not "01").
func arrayIndex(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return n, err == nil && n < 1<<32-1
}

func (m *orderedMap) reorder() {
	sort.SliceStable(m.keys, func(i, j int) bool {
		a, aok := arrayIndex(m.keys[i])
		b, bok := arrayIndex(m.keys[j])
		switch {
		case aok && bok:
			return a < b
		case aok:
			return true
		}
		return false
	})
}

// UnmarshalJSON accepts an object of strings. Non-string values are dropped, as npm drops
// them when it normalises a manifest; anything but an object or null is an error.
func (m *orderedMap) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		// npm tolerates garbage here (an array, a string) by ignoring it.
		return nil
	}
	m.vals = map[string]string{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		var v string
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		m.Set(k, v)
	}
	_, err = dec.Token()
	return err
}

// peerMeta is peerDependenciesMeta.
type peerMeta map[string]struct {
	Optional bool `json:"optional"`
}

// UnmarshalJSON tolerates malformed entries.
func (p *peerMeta) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	*p = peerMeta{}
	for k, v := range raw {
		var e struct {
			Optional bool `json:"optional"`
		}
		_ = json.Unmarshal(v, &e)
		(*p)[k] = e
	}
	return nil
}

// manifest is one version of a package as the registry's abbreviated packument describes it,
// normalised the way npm normalises it before building nodes (@npmcli/package-json
// normalizeSteps: bundleDependencies, optionalDedupe; npm-normalize-package-bin).
type manifest struct {
	Name                 string          `json:"name"`
	Version              string          `json:"version"`
	Dependencies         *orderedMap     `json:"dependencies"`
	OptionalDependencies *orderedMap     `json:"optionalDependencies"`
	PeerDependencies     *orderedMap     `json:"peerDependencies"`
	PeerDependenciesMeta peerMeta        `json:"peerDependenciesMeta"`
	BundleDependencies   json.RawMessage `json:"bundleDependencies"`
	BundledDependencies  json.RawMessage `json:"bundledDependencies"`
	OS                   json.RawMessage `json:"os"`
	CPU                  json.RawMessage `json:"cpu"`
	Libc                 json.RawMessage `json:"libc"`
	HasInstallScript     bool            `json:"hasInstallScript"`
	Deprecated           json.RawMessage `json:"deprecated"`
	Bin                  json.RawMessage `json:"bin"`
	Engines              json.RawMessage `json:"engines"`
	HasShrinkwrap        bool            `json:"_hasShrinkwrap"`
	Dist                 struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	} `json:"dist"`

	// importers only: devDependencies (registry manifests never load them)
	devDependencies *orderedMap

	// derived by normalize
	bundled    []string
	bins       map[string]string
	deprecated bool
	nodeEngine string
	os, cpu    []string
	libc       []string
	normalized bool
}

func stringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return l
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []string{s}
	}
	return nil
}

func (m *manifest) normalize() {
	if m.normalized {
		return
	}
	m.normalized = true
	bd := m.BundleDependencies
	if len(bd) == 0 || string(bd) == "null" {
		bd = m.BundledDependencies
	}
	var all bool
	if json.Unmarshal(bd, &all) == nil {
		if all {
			m.bundled = append(m.bundled, m.Dependencies.Keys()...)
		}
	} else {
		var l []string
		if json.Unmarshal(bd, &l) == nil {
			m.bundled = l
		} else {
			var obj orderedMap
			if json.Unmarshal(bd, &obj) == nil {
				m.bundled = obj.Keys()
			}
		}
	}
	if m.Dependencies != nil && m.OptionalDependencies != nil {
		for _, k := range m.OptionalDependencies.Keys() {
			m.Dependencies.Delete(k)
		}
	}
	m.deprecated = len(m.Deprecated) > 0 && !bytes.Equal(m.Deprecated, []byte("false")) &&
		!bytes.Equal(m.Deprecated, []byte(`""`)) && !bytes.Equal(m.Deprecated, []byte("null"))
	var eng map[string]json.RawMessage
	if json.Unmarshal(m.Engines, &eng) == nil {
		var s string
		if json.Unmarshal(eng["node"], &s) == nil {
			m.nodeEngine = s
		} else if len(eng["node"]) > 0 && string(eng["node"]) != "null" {
			m.nodeEngine = "\x00invalid" // present but not a string: never satisfied
		}
	}
	m.os, m.cpu, m.libc = stringList(m.OS), stringList(m.CPU), stringList(m.Libc)
	m.bins = normalizeBin(m.Name, m.Bin)
}

// normalizeBin is npm-normalize-package-bin: string → {basename(name): path}, array →
// {basename(p): p}, object → sanitised keys and paths (no "..", no leading "/").
func normalizeBin(name string, raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	in := map[string]string{}
	var s string
	var arr []string
	var obj map[string]json.RawMessage
	switch {
	case json.Unmarshal(raw, &s) == nil:
		if s == "" || name == "" {
			return nil
		}
		in[name] = s
	case json.Unmarshal(raw, &arr) == nil:
		for _, k := range arr {
			in[path.Base(k)] = k
		}
	case json.Unmarshal(raw, &obj) == nil:
		for k, v := range obj {
			var t string
			if json.Unmarshal(v, &t) == nil {
				in[k] = t
			}
		}
	default:
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		k = strings.NewReplacer(`\`, "/", ":", "/").Replace(k)
		base := strings.TrimPrefix(path.Join("/", path.Base(k)), "/")
		if base == "" || base == "." {
			continue
		}
		target := strings.TrimPrefix(path.Join("/", strings.ReplaceAll(v, `\`, "/")), "/")
		if target == "" {
			continue
		}
		out[base] = target
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// packument is the abbreviated registry document of one package.
type packument struct {
	Name     string               `json:"name"`
	DistTags map[string]string    `json:"dist-tags"`
	Versions map[string]*manifest `json:"versions"`

	versions []pv // parsed, valid versions
}

type pv struct {
	raw string
	v   *Version
	m   *manifest
}

// prepare validates the document for name and parses its versions. Versions whose key and
// manifest disagree, or that are not semver, are dropped (npm could never pick them).
func (p *packument) prepare(name string) error {
	if p.Name != "" && p.Name != name {
		return fmt.Errorf("registry document for %s names %q", name, p.Name)
	}
	for k, m := range p.Versions {
		if m == nil {
			continue
		}
		v, err := ParseVersion(k, true)
		if err != nil || (m.Version != "" && m.Version != k) || (m.Name != "" && m.Name != name) {
			continue
		}
		m.Version, m.Name = k, name
		p.versions = append(p.versions, pv{raw: k, v: v, m: m})
	}
	sort.Slice(p.versions, func(i, j int) bool { return p.versions[i].raw < p.versions[j].raw })
	return nil
}
