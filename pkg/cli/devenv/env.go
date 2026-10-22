package devenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
)

// File is .bob/env.json (SPEC §02.7.2). Drvs and Packages are bob's own additions: what bob exec
// realises when a closure path is missing, and the summary line of bob dev.
type File struct {
	V          int               `json:"v"`
	Root       string            `json:"root"`
	Shell      string            `json:"shell"`
	EvalKey    string            `json:"evalKey"`
	Inside     map[string]string `json:"inside"`
	Outside    Outside           `json:"outside"`
	InsideOnly []string          `json:"insideOnly"`
	Tools      map[string]Tool   `json:"tools"`
	Closure    []string          `json:"closure"`
	Hooks      []string          `json:"hooks"`
	Servers    []Server          `json:"servers"`
	Drvs       map[string]string `json:"drvs"`     // closure path → derivation that realises it
	Packages   []Package         `json:"packages"` // top-level packages, PATH order
}

// Outside are the variables exported outside the namespace.
type Outside struct {
	Set map[string]string `json:"set"`
}

// Tool is one shimmed tool.
type Tool struct {
	Path string            `json:"path"`
	LSP  bool              `json:"lsp"`
	Env  map[string]string `json:"env"`
}

// Server is one dev server (engine.DevServerSpec as recorded).
type Server struct {
	Name    string            `json:"name"`
	Label   string            `json:"label,omitempty"`
	Cmd     []string          `json:"cmd"`
	Cwd     string            `json:"cwd"`
	Ready   string            `json:"ready"`
	Port    int               `json:"port,omitempty"`
	Host    string            `json:"host,omitempty"`
	Pattern string            `json:"pattern,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Package is one top-level package for the summary line.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path"`
}

// LSPServers are the tools started behind the path-rewriting proxy (SPEC §02.7.4 table).
var lspServers = map[string]bool{
	"gopls": true, "clangd": true, "rust-analyzer": true, "typescript-language-server": true,
	"vtsls": true, "ocamllsp": true, "vscode-eslint-language-server": true,
	"vscode-json-language-server": true, "svelteserver": true, "tailwindcss-language-server": true,
}

// IsLSP reports whether tool is an LSP server of the §02.7.4 table.
func IsLSP(tool string) bool { return lspServers[tool] }

// Spec is what a dev shell resolved to (the engine's build.DevResult, in the CLI's terms).
type Spec struct {
	Root      string
	Shell     string
	EvalKey   string
	Packages  []engine.DevPackage
	Env       map[string]string // exported outside when store-free (ecosystem Env + shell Env)
	InsideEnv map[string]string // inside only
	ToolEnv   map[string]map[string]string
	Tools     map[string]string // in-tree tools: name → absolute path
	Hooks     []string
	Servers   []engine.DevServerSpec
}

const storeDir = "/nix/store"

var (
	toolName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	storeTool = regexp.MustCompile(`^/nix/store/[^/]+/bin/([^/]+)$`)
)

// Compose turns s into env.json. read maps a store path to where this process can read it (the
// path itself inside bob's namespace, the host path outside); the packages must be realised.
func Compose(s Spec, read func(string) string) *File {
	f := &File{V: 1, Root: s.Root, Shell: s.Shell, EvalKey: s.EvalKey, Inside: map[string]string{},
		Outside: Outside{Set: map[string]string{}}, InsideOnly: []string{}, Tools: map[string]Tool{},
		Closure: []string{}, Hooks: append([]string{}, s.Hooks...), Servers: []Server{}, Drvs: map[string]string{},
		Packages: []Package{}}
	var path []string
	seen := map[string]bool{}
	for _, p := range s.Packages {
		if p.Path == "" || seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		f.Closure = append(f.Closure, p.Path)
		if p.Drv != "" {
			f.Drvs[p.Path] = p.Drv
		}
		f.Packages = append(f.Packages, Package{Name: p.Name, Version: p.Version, Path: p.Path})
		bin := p.Path + "/bin"
		ents, err := os.ReadDir(read(bin))
		if err != nil {
			continue
		}
		path = append(path, bin)
		for _, e := range ents {
			name := e.Name()
			if _, ok := f.Tools[name]; ok || !toolName.MatchString(name) {
				continue
			}
			if !executable(read(bin + "/" + name)) {
				continue
			}
			f.Tools[name] = Tool{Path: bin + "/" + name}
		}
	}
	for name, p := range s.Tools {
		if _, ok := f.Tools[name]; ok || !toolName.MatchString(name) {
			continue
		}
		f.Tools[name] = Tool{Path: p}
		if d := filepath.Dir(p); !contains(path, d) {
			path = append(path, d)
		}
	}
	for name, t := range f.Tools {
		t.LSP = lspServers[name]
		t.Env = map[string]string{}
		for k, v := range s.ToolEnv[name] {
			t.Env[k] = v
		}
		f.Tools[name] = t
	}
	for k, v := range s.InsideEnv {
		f.Inside[k] = v
		if strings.Contains(v, storeDir) {
			f.InsideOnly = append(f.InsideOnly, k)
		}
	}
	for k, v := range s.Env {
		if k == "PATH" {
			continue // the dev shell's PATH is bob's
		}
		f.Inside[k] = v
		switch m := storeTool.FindStringSubmatch(v); {
		case !strings.Contains(v, storeDir):
			f.Outside.Set[k] = v
		case m != nil && f.Tools[m[1]].Path != "":
			f.Outside.Set[k] = filepath.Join(s.Root, ".bob", "bin", m[1])
		default:
			if !contains(f.InsideOnly, k) {
				f.InsideOnly = append(f.InsideOnly, k)
			}
		}
	}
	f.Inside["PATH"] = strings.Join(path, ":")
	sort.Strings(f.InsideOnly)
	for _, sv := range s.Servers {
		f.Servers = append(f.Servers, Server{Name: sv.Name, Label: sv.Label, Cmd: sv.Cmd, Cwd: sv.Cwd, Ready: sv.Ready,
			Port: sv.Port, Host: sv.Host, Pattern: sv.Pattern, Env: sv.Env})
	}
	return f
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Encode is the file's bytes: indented JSON, keys sorted, no HTML escaping.
func (f *File) Encode() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Hash is BOB_ENV_HASH of env.json's bytes: sha256, 16 hex digits.
func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// maxEnvFile bounds the env.json bob reads (it is bob's own file, but lives in the working tree).
const maxEnvFile = 16 << 20

// Path is the env file of shell in root: .bob/env.json for the default shell, else
// .bob/env.<shell>.json (shims always belong to the default shell).
func Path(root, shell string) string {
	if shell == "" || shell == "default" {
		return filepath.Join(root, ".bob", "env.json")
	}
	return filepath.Join(root, ".bob", "env."+shell+".json")
}

// Read reads and decodes an env file; it also returns its bytes.
func Read(p string) (*File, []byte, error) {
	b, err := readCapped(p, maxEnvFile)
	if err != nil {
		return nil, nil, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p, err)
	}
	if f.V != 1 {
		return nil, nil, fmt.Errorf("%s: unknown version %d", p, f.V)
	}
	return &f, b, nil
}

func readCapped(p string, max int64) ([]byte, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	return b, nil
}

// WriteFile writes b to p atomically (temp file + rename) unless p already holds b. It reports
// whether the file changed.
func WriteFile(p string, b []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), p)
}

// Shim is the text of the shim of tool (SPEC §02.7.3).
func Shim(bobPath, root, tool string, lsp bool) string {
	flag := ""
	if lsp {
		flag = "--lsp "
	}
	return fmt.Sprintf("#!/bin/sh\n# Code generated by bob. DO NOT EDIT.\nexec \"${BOB:-%s}\" exec %s--project %s -- %s \"$@\"\n",
		dquoteEsc(bobPath), flag, Quote(root), tool)
}

// WriteShims makes root/.bob/bin hold exactly the shims of f's tools (mode 0755).
func WriteShims(root, bobPath string, f *File) error {
	dir := filepath.Join(root, ".bob", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, t := range f.Tools {
		if _, err := WriteFile(filepath.Join(dir, name), []byte(Shim(bobPath, root, name, t.LSP)), 0o755); err != nil {
			return err
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if _, ok := f.Tools[e.Name()]; !ok {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Quote is POSIX single quoting.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// dquoteEsc escapes s for a double-quoted POSIX shell word.
func dquoteEsc(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return r.Replace(s)
}
