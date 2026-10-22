package resolve

import (
	"path/filepath"
	"strings"
)

// WorkspaceDeps returns, per workspace module with sources (Dir set), the other workspace
// modules of the repository whose packages it imports: from its packages and their tests,
// then through the non-test imports of the workspace packages reached (cmd/go's "all"). The
// go command finds such a module only through a requirement with a directory replace, which
// bob writes into the importing module's go.mod (§03.2.5, R-GO-002b); its own requirements
// are part of the importer's roots already (Resolve). Third-party packages are not followed.
func WorkspaceDeps(wss []Workspace) (map[string][]string, error) {
	byPath := map[string]*Workspace{}
	for i := range wss {
		byPath[wss[i].Path] = &wss[i]
	}
	out := map[string][]string{}
	for i := range wss {
		s := &wss[i]
		if s.Dir == "" {
			continue
		}
		deps := map[string]bool{}
		seen := map[string]bool{}
		var queue []string
		push := func(ips []string) {
			for _, ip := range ips {
				if !seen[ip] && !isStd(ip) {
					seen[ip] = true
					queue = append(queue, ip)
				}
			}
		}
		dirs, err := workspaceDirs(s.Dir)
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			pi, err := readImports(diskDir(filepath.Join(s.Dir, filepath.FromSlash(d))))
			if err != nil {
				return nil, err
			}
			seen[joinPath(s.Path, d)] = true
			push(pi.imports)
			push(pi.tests)
		}
		for len(queue) > 0 {
			ip := queue[0]
			queue = queue[1:]
			w := longestWorkspace(byPath, ip)
			if w == nil {
				continue
			}
			if w.Path != s.Path {
				deps[w.Path] = true
			}
			if w.Dir == "" || generated(w.Generated, ip) {
				continue
			}
			pi, err := readImports(diskDir(filepath.Join(w.Dir, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(ip, w.Path), "/")))))
			if err != nil {
				return nil, err
			}
			push(pi.imports)
		}
		if len(deps) > 0 {
			out[s.Path] = sortedSet(deps)
		}
	}
	return out, nil
}

// generated reports whether ip is inside one of the generated package trees gens (a codegen
// output may hold several packages, e.g. greetv1 and greetv1/greetv1connect).
func generated(gens []string, ip string) bool {
	for _, g := range gens {
		if ip == g || strings.HasPrefix(ip, g+"/") {
			return true
		}
	}
	return false
}
