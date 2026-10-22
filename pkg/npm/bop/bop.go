package bop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/builder"
	"github.com/megakuul/bob/pkg/builder/bspec"
)

// Kinds returns the npm job kinds (SPEC §03.4.3); cmd/bob-builder adds them to builder.Core().
// npm outputs are package trees and bundles: the binary fixup (strip, RUNPATH) does not apply,
// prebuilt binaries inside packages must stay byte-exact.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{
		"npm-unpack":       {Run: unpackKind, NoFixup: true},
		"npm-node-modules": {Run: nodeModulesKind, NoFixup: true},
		"npm-build":        {Run: buildKind, NoFixup: true},
	}
}

// Link is a symlink <dir>/node_modules/<Alias> → Target.
type Link struct {
	Alias  string `json:"alias"`
	Target string `json:"target"`
}

// Bin is one command of a dependency: Script is the absolute path of its JavaScript file.
type Bin struct {
	Name   string `json:"name"`
	Script string `json:"script"`
}

// Script is how an allowed package's lifecycle scripts run (R-NPM-004).
type Script struct {
	Node string            `json:"node"` // nodejs root: bin/node, include/node, npm's node-gyp
	Bash string            `json:"bash"` // bash binary
	Path []string          `json:"path"` // tool bin dirs (python3, make, cc, sed, grep, coreutils)
	Bins []Bin             `json:"bins"` // the package's dependencies' commands
	Env  map[string]string `json:"env"`  // quirk environment
}

// Member is one package of an npm-unpack job: the tarball unpacked into
// <Dir>/node_modules/<Name>, its dependencies linked next to it.
type Member struct {
	Dir     string  `json:"dir"`
	Name    string  `json:"name"`
	Tarball string  `json:"tarball"`
	Links   []Link  `json:"links"`
	Script  *Script `json:"script,omitempty"`
}

// UnpackArgs is the Args schema of kind npm-unpack: one package instance, or the members of a
// dependency cycle (one derivation, Nix forbids reference cycles).
type UnpackArgs struct {
	Members []Member `json:"members"`
}

// HoistEntry is one directory of a hoisted node_modules tree.
type HoistEntry struct {
	Path    string  `json:"path"`              // relative to Out ("react", "a/node_modules/b")
	Tarball string  `json:"tarball,omitempty"` // registry package
	Link    string  `json:"link,omitempty"`    // workspace library: a symlink instead
	Script  *Script `json:"script,omitempty"`
}

// NodeModulesArgs is the Args schema of kind npm-node-modules: an importer's node_modules,
// either symlinks to package instances (isolated) or an npm-style tree (hoisted).
type NodeModulesArgs struct {
	Out     string       `json:"out"`
	Links   []Link       `json:"links"`
	Hoisted []HoistEntry `json:"hoisted"`
	Bins    []Bin        `json:"bins"` // .bin/<name> wrappers
	Node    string       `json:"node"` // node binary the wrappers run
	Bash    string       `json:"bash"` // interpreter of the wrappers
}

// Step is one operation of an npm-build job.
type Step struct {
	Op      string            `json:"op"`                // mkdir | copy | write | symlink | exec | scrub
	Path    string            `json:"path"`              // the path written (exec: the working directory)
	From    string            `json:"from,omitempty"`    // copy
	Target  string            `json:"target,omitempty"`  // symlink
	Content string            `json:"content,omitempty"` // write
	Mode    uint32            `json:"mode,omitempty"`    // write
	Argv    []string          `json:"argv,omitempty"`    // exec
	Env     map[string]string `json:"env,omitempty"`     // exec
	Dirs    []string          `json:"dirs,omitempty"`    // exec: PATH
}

// BuildArgs is the Args schema of kind npm-build: tsc, Vite, esbuild and Bun builds, and test
// workspaces.
type BuildArgs struct {
	Steps []Step `json:"steps"`
}

func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return builder.Invalidf("npm job args: %v", err)
	}
	return nil
}

// validAlias rejects names that would leave node_modules.
func validAlias(a string) bool {
	if a == "" || strings.ContainsRune(a, 0) || strings.Contains(a, "\\") {
		return false
	}
	parts := strings.Split(a, "/")
	if len(parts) > 2 || len(parts) == 2 && !strings.HasPrefix(parts[0], "@") {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || p == "@" {
			return false
		}
	}
	return true
}

func unpackKind(c *builder.Ctx, raw json.RawMessage) error {
	var a UnpackArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if len(a.Members) == 0 {
		return builder.Invalidf("npm-unpack: no members")
	}
	c.Phase("unpack")
	for _, m := range a.Members {
		if !validAlias(m.Name) || !filepath.IsAbs(m.Tarball) {
			return builder.Invalidf("npm-unpack: invalid member %q", m.Name)
		}
		nm := filepath.Join(m.Dir, "node_modules")
		if err := c.Unpack(m.Tarball, filepath.Join(nm, filepath.FromSlash(m.Name)), 1); err != nil {
			return err
		}
		if err := links(c, nm, m.Links, m.Name); err != nil {
			return err
		}
	}
	for _, m := range a.Members {
		if m.Script == nil {
			continue
		}
		c.Phase("install script " + m.Name)
		pkg := filepath.Join(m.Dir, "node_modules", filepath.FromSlash(m.Name))
		if err := runScripts(c, pkg, m.Name, *m.Script); err != nil {
			return err
		}
	}
	return nil
}

// links creates node_modules/<alias> → target for every link except the package itself.
func links(c *builder.Ctx, nm string, ls []Link, self string) error {
	for _, l := range ls {
		if !validAlias(l.Alias) || !filepath.IsAbs(l.Target) {
			return builder.Invalidf("npm link %q → %q is invalid", l.Alias, l.Target)
		}
		if l.Alias == self {
			continue
		}
		p := filepath.Join(nm, filepath.FromSlash(l.Alias))
		if err := c.MkdirAll(filepath.Dir(p)); err != nil {
			return err
		}
		w, err := c.Writable(p)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(w); err == nil {
			continue // the tarball brings its own copy (bundled dependency)
		}
		if err := os.Symlink(l.Target, w); err != nil {
			return err
		}
	}
	return nil
}

// wrapper is a .bin entry: node runs the script by its real path (the sandbox has no
// /usr/bin/env, so shebangs of packages cannot be used).
func wrapper(bash, node, script string) string {
	return "#!" + bash + "\nexec " + shellQuote(node) + " " + shellQuote(script) + " \"$@\"\n"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func writeExec(c *builder.Ctx, p, content string) error {
	w, err := c.Writable(p)
	if err != nil {
		return err
	}
	if err := c.MkdirAll(filepath.Dir(w)); err != nil {
		return err
	}
	return os.WriteFile(w, []byte(content), 0o755)
}

// lifecycle reads the install scripts of the package in dir in npm's order (preinstall,
// install, postinstall); a package with binding.gyp and neither install nor preinstall gets
// npm's implicit "node-gyp rebuild".
func lifecycle(dir string) ([][2]string, map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, nil, err
	}
	var pj map[string]any
	if err := json.Unmarshal(b, &pj); err != nil {
		return nil, nil, fmt.Errorf("%s/package.json: %w", filepath.Base(dir), err)
	}
	scripts, _ := pj["scripts"].(map[string]any)
	get := func(k string) string { s, _ := scripts[k].(string); return s }
	var out [][2]string
	if s := get("preinstall"); s != "" {
		out = append(out, [2]string{"preinstall", s})
	}
	install := get("install")
	if install == "" && get("preinstall") == "" {
		if _, err := os.Stat(filepath.Join(dir, "binding.gyp")); err == nil {
			install = "node-gyp rebuild"
		}
	}
	if install != "" {
		out = append(out, [2]string{"install", install})
	}
	if s := get("postinstall"); s != "" {
		out = append(out, [2]string{"postinstall", s})
	}
	return out, pj, nil
}

// runScripts runs the allowed lifecycle scripts of the package in dir offline (the sandbox has
// no network) and keeps only build/Release/*.node of the build tree afterwards.
func runScripts(c *builder.Ctx, dir, name string, s Script) error {
	if !filepath.IsAbs(s.Node) || !filepath.IsAbs(s.Bash) {
		return builder.Invalidf("npm script of %s: node and bash must be absolute paths", name)
	}
	scripts, pj, err := lifecycle(dir)
	if err != nil || len(scripts) == 0 {
		return err
	}
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	bin := filepath.Join(tmp, "npm-bin-"+strings.NewReplacer("/", "-", "@", "").Replace(name))
	node := filepath.Join(s.Node, "bin", "node")
	for _, b := range s.Bins {
		if !validAlias(b.Name) || strings.Contains(b.Name, "/") || !filepath.IsAbs(b.Script) {
			return builder.Invalidf("npm script of %s: invalid command %q", name, b.Name)
		}
		if err := writeExec(c, filepath.Join(bin, b.Name), wrapper(s.Bash, node, b.Script)); err != nil {
			return err
		}
	}
	gyp := filepath.Join(s.Node, "lib", "node_modules", "npm", "node_modules", "node-gyp", "bin", "node-gyp.js")
	if err := writeExec(c, filepath.Join(bin, "node-gyp"), wrapper(s.Bash, node, gyp)); err != nil {
		return err
	}
	version, _ := pj["version"].(string)
	path := append([]string{bin, filepath.Join(s.Node, "bin"), filepath.Dir(s.Bash)}, s.Path...)
	for _, sc := range scripts {
		env := map[string]string{
			"npm_config_nodedir": s.Node, "npm_package_name": name, "npm_package_version": version,
			"npm_lifecycle_event": sc[0], "npm_lifecycle_script": sc[1], "npm_config_cache": filepath.Join(tmp, "npm-cache"),
			"npm_config_offline": "true", "npm_node_execpath": node, "NODE": node, "npm_config_user_agent": "bob",
			"JOBS": "max",
		}
		for k, v := range s.Env {
			env[k] = v
		}
		c.Logf("%s: %s: %s", name, sc[0], sc[1])
		if err := c.Exec(bspec.ExecArgs{Argv: []string{s.Bash, "-euo", "pipefail", "-c", sc[1]}, Env: env, Cwd: dir, Path: path}); err != nil {
			return fmt.Errorf("%s: %s script %q failed: %w", name, sc[0], sc[1], err)
		}
	}
	return pruneBuild(filepath.Join(dir, "build"))
}

// pruneBuild keeps only build/Release/*.node: the rest of a node-gyp build tree (Makefiles,
// objects, config.gypi) references the toolchain and node.
func pruneBuild(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if filepath.Dir(p) == filepath.Join(root, "Release") && strings.HasSuffix(p, ".node") && d.Type().IsRegular() {
			return nil
		}
		return os.Remove(p)
	})
	if err != nil {
		return err
	}
	if err := dropUnused(root); err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		_ = os.Remove(d) // only empty directories go
	}
	return nil
}

// dropUnused removes the addons of build/Release that the package's JavaScript never names
// (better-sqlite3 also builds test_extension.node for its own tests). When the package names
// none of them (a computed name), all are kept.
func dropUnused(root string) error {
	addons, err := filepath.Glob(filepath.Join(root, "Release", "*.node"))
	if err != nil || len(addons) < 2 {
		return err
	}
	pkg := filepath.Dir(root)
	named := map[string]bool{}
	err = filepath.WalkDir(pkg, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == root || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		switch filepath.Ext(p) {
		case ".js", ".cjs", ".mjs":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, a := range addons {
			if bytes.Contains(b, []byte(strings.TrimSuffix(filepath.Base(a), ".node"))) {
				named[a] = true
			}
		}
		return nil
	})
	if err != nil || len(named) == 0 {
		return err
	}
	for _, a := range addons {
		if !named[a] {
			if err := os.Remove(a); err != nil {
				return err
			}
		}
	}
	return nil
}

func nodeModulesKind(c *builder.Ctx, raw json.RawMessage) error {
	var a NodeModulesArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	out, err := c.Writable(a.Out)
	if err != nil {
		return err
	}
	if err := c.MkdirAll(out); err != nil {
		return err
	}
	c.Phase("node_modules")
	for _, e := range a.Hoisted {
		rel := filepath.FromSlash(e.Path)
		if e.Path == "" || filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
			return builder.Invalidf("npm-node-modules: invalid hoisted path %q", e.Path)
		}
		p := filepath.Join(out, rel)
		switch {
		case e.Link != "":
			if err := c.MkdirAll(filepath.Dir(p)); err != nil {
				return err
			}
			w, err := c.Writable(p)
			if err != nil {
				return err
			}
			if err := os.Symlink(e.Link, w); err != nil {
				return err
			}
		default:
			if err := c.Unpack(e.Tarball, p, 1); err != nil {
				return err
			}
		}
	}
	for _, e := range a.Hoisted {
		if e.Script == nil {
			continue
		}
		name := e.Path
		if i := strings.LastIndex(name, "node_modules/"); i >= 0 {
			name = name[i+len("node_modules/"):]
		}
		c.Phase("install script " + name)
		if err := runScripts(c, filepath.Join(out, filepath.FromSlash(e.Path)), name, *e.Script); err != nil {
			return err
		}
	}
	if err := links(c, out, a.Links, ""); err != nil {
		return err
	}
	for _, b := range a.Bins {
		if !validAlias(b.Name) || strings.Contains(b.Name, "/") || !filepath.IsAbs(b.Script) {
			return builder.Invalidf("npm-node-modules: invalid command %q", b.Name)
		}
		if err := writeExec(c, filepath.Join(out, ".bin", b.Name), wrapper(a.Bash, a.Node, b.Script)); err != nil {
			return err
		}
	}
	return nil
}

func buildKind(c *builder.Ctx, raw json.RawMessage) error {
	var a BuildArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	for i, s := range a.Steps {
		if err := step(c, s); err != nil {
			return fmt.Errorf("step %d (%s %s): %w", i+1, s.Op, s.Path, err)
		}
	}
	return nil
}

func step(c *builder.Ctx, s Step) error {
	switch s.Op {
	case "mkdir":
		return c.MkdirAll(s.Path)
	case "copy":
		return c.Copy(s.From, s.Path)
	case "write":
		w, err := c.Writable(s.Path)
		if err != nil {
			return err
		}
		if err := c.MkdirAll(filepath.Dir(w)); err != nil {
			return err
		}
		mode := fs.FileMode(s.Mode)
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(w, []byte(s.Content), mode&0o777)
	case "symlink":
		w, err := c.Writable(s.Path)
		if err != nil {
			return err
		}
		if s.Target == "" {
			return builder.Invalidf("symlink without target")
		}
		if err := c.MkdirAll(filepath.Dir(w)); err != nil {
			return err
		}
		return os.Symlink(s.Target, w)
	case "exec":
		if len(s.Argv) == 0 {
			return builder.Invalidf("exec without argv")
		}
		c.Phase(filepath.Base(s.Argv[0]))
		return c.Exec(bspec.ExecArgs{Argv: s.Argv, Env: s.Env, Cwd: s.Path, Path: s.Dirs})
	case "scrub":
		return scrub(c, s.Path)
	}
	return builder.Invalidf("unknown npm-build op %q", s.Op)
}

// npmRef matches the hash part of store paths of bob's npm derivations (package instances,
// node_modules, libraries). Bundlers keep them in comments and module keys; they would put
// every package into the runtime closure (S4-8).
var npmRef = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`/nix/store/[0-9a-df-np-sv-z]{32}-(npm-|node_modules-)`)
})

// scrub replaces the hash part of npm store paths in the files below root with e's.
func scrub(c *builder.Ctx, root string) error {
	w, err := c.Writable(root)
	if err != nil {
		return err
	}
	fake := "/nix/store/" + strings.Repeat("e", 32) + "-"
	return filepath.WalkDir(w, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		nb := npmRef().ReplaceAllFunc(b, func(m []byte) []byte {
			return append([]byte(fake), m[len(fake):]...)
		})
		if bytes.Equal(b, nb) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(p, nb, fi.Mode().Perm())
	})
}
