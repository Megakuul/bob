package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// registry is one entry of config.json "registries" (SPEC §02.8 Files, R-INST-004).
type registry struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	PublicKeys []string `json:"publicKeys"`
}

// installConfig is the part of config.json bob install and bob registry read.
type installConfig struct {
	Registries      []registry `json:"registries"`
	DefaultRegistry string     `json:"defaultRegistry"`
	BinDir          string     `json:"binDir"`
}

func (e *env) installConfig() installConfig {
	var c installConfig
	if dir := configDir(e.getenv); dir != "" {
		_ = readJSON(filepath.Join(dir, configName), &c)
	}
	return c
}

// defaultBinDir is config.json binDir (a leading ~/ is the home directory), else ~/.local/bin.
func (e *env) defaultBinDir() string {
	home := e.getenv("HOME")
	if d := e.installConfig().BinDir; d != "" {
		if rest, ok := strings.CutPrefix(d, "~/"); ok && home != "" {
			return filepath.Join(home, rest)
		}
		if filepath.IsAbs(d) {
			return filepath.Clean(d)
		}
	}
	return filepath.Join(home, ".local", "bin")
}

// lookupRegistry returns the registry named name, or the default one for "": defaultRegistry,
// else the registry named "default", else the only one.
func (c installConfig) lookupRegistry(name string) (registry, bool) {
	if name == "" {
		name = c.DefaultRegistry
		if name == "" {
			name = "default"
		}
		if len(c.Registries) == 1 && c.DefaultRegistry == "" {
			return c.Registries[0], true
		}
	}
	for _, r := range c.Registries {
		if r.Name == name {
			return r, true
		}
	}
	return registry{}, false
}

// slug is a registry, project or release name.
var slug = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`) })

// runRegistry is `bob registry add <name> <url> [--key K…] [--refresh-keys] | remove <name> |
// list | default <name>` (R-INST-004).
func runRegistry(e *env) int {
	pos := e.a.pos
	if len(pos) == 0 {
		return e.out.fail(diag.New(4, "flag", "bob registry", "what", "add, remove, list or default"))
	}
	switch pos[0] {
	case "add":
		return registryAdd(e, pos[1:])
	case "remove", "default":
		if len(pos) != 2 {
			return e.out.fail(diag.New(4, "flag", "bob registry "+pos[0], "what", "a registry name"))
		}
		name := pos[1]
		if _, ok := e.installConfig().lookupRegistry(name); !ok {
			return e.out.fail(diag.New(1, "kind", "registry", "name", name))
		}
		err := e.updateConfig(func(cfg map[string]json.RawMessage) bool {
			if pos[0] == "default" {
				cfg["defaultRegistry"], _ = json.Marshal(name)
				return true
			}
			var regs []json.RawMessage
			_ = json.Unmarshal(cfg["registries"], &regs)
			var kept []json.RawMessage
			for _, r := range regs {
				var rr registry
				if json.Unmarshal(r, &rr) == nil && rr.Name == name {
					continue
				}
				kept = append(kept, r)
			}
			if kept == nil {
				kept = []json.RawMessage{}
			}
			cfg["registries"], _ = json.Marshal(kept)
			var def string
			if json.Unmarshal(cfg["defaultRegistry"], &def) == nil && def == name {
				delete(cfg, "defaultRegistry")
			}
			return true
		})
		if err != nil {
			return e.out.fail(err)
		}
		e.out.result(map[string]string{pos[0]: name})
		return e.out.finish(0, translate.Counts{}, nil)
	case "list":
		c := e.installConfig()
		def, _ := c.lookupRegistry("")
		type row struct {
			registry
			Default bool `json:"default"`
		}
		rows := []row{}
		var b strings.Builder
		tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
		for _, r := range c.Registries {
			rows = append(rows, row{r, r.Name == def.Name})
			mark := ""
			if r.Name == def.Name {
				mark = "(default)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d key(s)\t%s\n", r.Name, r.URL, len(r.PublicKeys), mark)
		}
		_ = tw.Flush()
		e.out.result(map[string]any{"registries": rows})
		e.out.printf("%s", trimRows(b.String()))
		return e.out.finish(0, translate.Counts{}, nil)
	}
	return e.out.fail(diag.New(1, "kind", "registry subcommand", "name", pos[0]))
}

// trimRows drops the trailing blanks tabwriter leaves on each line.
func trimRows(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// registryAdd pins the registry's cache keys: --key values, else AuthService/GetServerInfo
// (anonymous, trust on first use). --refresh-keys re-reads the keys of an existing registry.
func registryAdd(e *env, pos []string) int {
	if len(pos) == 0 || len(pos) > 2 {
		return e.out.fail(diag.New(4, "flag", "bob registry add", "what", "<name> <url>"))
	}
	name := pos[0]
	if !slug().MatchString(name) {
		return e.out.fail(diag.New(4, "flag", "bob registry add", "what", "a registry name of letters, digits, '.', '_' and '-'"))
	}
	old, exists := e.installConfig().lookupRegistry(name)
	raw := ""
	if len(pos) == 2 {
		raw = pos[1]
	} else if exists && e.a.bool("refresh-keys") {
		raw = old.URL
	} else {
		return e.out.fail(diag.New(4, "flag", "bob registry add "+name, "what", "a URL"))
	}
	url, err := normServer(raw)
	if err != nil {
		return e.out.fail(err)
	}
	if exists && old.URL != url {
		return e.out.fail(diag.New(4, "flag", "bob registry add "+name, "what", "a new name ("+name+" is "+old.URL+"; remove it first)"))
	}
	keys := e.a.vals["key"]
	for _, k := range keys {
		if !nixKey().MatchString(k) {
			return e.out.fail(diag.New(4, "flag", "--key", "what", "a key of the form name:base64"))
		}
	}
	if len(keys) == 0 {
		ctx, cancel := context.WithTimeout(e.ctx, time.Minute)
		defer cancel()
		info, err := e.api(url, "").GetServerInfo(ctx)
		if err != nil {
			return e.out.fail(apiDiag(url, "bob.auth.v1.AuthService/GetServerInfo", "", err))
		}
		for _, k := range info.Nix.PublicKeys {
			if nixKey().MatchString(k) {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			d := diag.New(4004, "cache", url, "what", "no signing key")
			return e.out.fail(d)
		}
	}
	reg := registry{Name: name, URL: url, PublicKeys: keys}
	err = e.updateConfig(func(cfg map[string]json.RawMessage) bool {
		var regs []json.RawMessage
		_ = json.Unmarshal(cfg["registries"], &regs)
		enc, _ := json.Marshal(reg)
		replaced := false
		for i, r := range regs {
			var rr registry
			if json.Unmarshal(r, &rr) == nil && rr.Name == name {
				regs[i], replaced = enc, true
			}
		}
		if !replaced {
			regs = append(regs, enc)
		}
		cfg["registries"], _ = json.Marshal(regs)
		return true
	})
	if err != nil {
		return e.out.fail(err)
	}
	e.out.result(reg)
	verb := "added"
	if exists {
		verb = "updated"
	}
	e.out.printf("%s registry %s (%s), %d key(s) pinned\n", verb, name, url, len(keys))
	return e.out.finish(0, translate.Counts{}, nil)
}

// pathHint is the one hint line of SPEC §02.8 step 7 when binDir is not on PATH.
func pathHint(e *env, binDir string) {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(d) == binDir {
			return
		}
	}
	dir := binDir
	if t := tildePath(binDir); t != binDir {
		dir = "$HOME" + strings.TrimPrefix(t, "~")
	}
	if !e.out.json {
		fmt.Fprintf(e.out.errw, "hint: %s is not on PATH; add this line to your shell's rc file: export PATH=\"%s:$PATH\"\n", tildePath(binDir), dir)
	}
}
