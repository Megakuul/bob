package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// websiteJSBudget caps the gzip size of the JavaScript a page of the website loads before it
// is interactive (its modulepreload and entry scripts plus their static imports). The site
// is prerendered HTML; JS only hydrates the build graph, search, copy buttons and the theme
// toggle (A411). Measured 2026-10-24: 45 kB for the front page.
const websiteJSBudget = 60_000

func init() {
	register(Stage{Name: "website", Order: 9, Run: runWebsite})
}

// runWebsite checks and builds website/, the static project site and docs (plan/progress/W1.md):
// banned dependencies, svelte-check, the prerendered build, no third-party assets and the
// first-load JS budget. It mirrors the web stage.
func runWebsite(ctx context.Context, env *Env) error {
	dir := filepath.Join(env.Root, "website")
	if err := checkWebDeps(dir, "website"); err != nil {
		return err
	}
	node, err := nixTool(ctx, env.Root, "nodejs_22")
	if err != nil {
		return err
	}
	nodeBin := filepath.Join(node, "bin", "node")
	pathEnv := []string{"PATH=" + filepath.Join(node, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); os.IsNotExist(err) {
		// From the committed lock file only, lifecycle scripts off (HARNESS.md §1.4); the npm
		// cache is tried first so a warm host installs without the network.
		if _, err := command(ctx, dir, pathEnv, filepath.Join(node, "bin", "npm"), "ci", "--ignore-scripts", "--prefer-offline", "--no-audit", "--no-fund"); err != nil {
			return harnessErr(err)
		}
	}
	steps := [][]string{
		{"node_modules/@sveltejs/kit/svelte-kit.js", "sync"},
		{"node_modules/svelte-check/bin/svelte-check", "--tsconfig", "./tsconfig.json", "--fail-on-warnings"},
	}
	if !env.Fast {
		steps = append(steps, []string{"node_modules/vite/bin/vite.js", "build"})
	}
	for _, s := range steps {
		if _, err := command(ctx, dir, pathEnv, nodeBin, s...); err != nil {
			return err
		}
	}
	if env.Fast {
		return nil
	}
	build := filepath.Join(dir, "build")
	if err := checkNoExternalAssets(build); err != nil {
		return err
	}
	worst, page, err := firstLoadJS(build)
	if err != nil {
		return err
	}
	fmt.Printf("website: largest first-load JS %d bytes gzip (%s), budget %d\n", worst, page, websiteJSBudget)
	if worst > websiteJSBudget {
		return fmt.Errorf("website: %s loads %d bytes of gzipped JS, budget %d", page, worst, websiteJSBudget)
	}
	return nil
}

var (
	reJSRef       = regexp.MustCompile(`["'(](?:\.{0,2}/)*(_app/immutable/[^"')]+\.js)["')]`)
	reStaticImp   = regexp.MustCompile(`(?:from|import)\s*["'](\.{1,2}/[^"']+\.js)["']`)
	reExternalRef = regexp.MustCompile(`(?i)(?:<script[^>]+src|<link[^>]+href|<img[^>]+src|@import|url\()\s*=?\s*["']?(?:https?:)?//`)
)

// checkNoExternalAssets fails when a built page or stylesheet loads anything from another
// origin (CDN scripts, web fonts, images): the site makes no third-party requests.
func checkNoExternalAssets(build string) error {
	var bad []string
	err := filepath.WalkDir(build, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := filepath.Ext(p); ext != ".html" && ext != ".css" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if m := reExternalRef.Find(b); m != nil {
			rel, _ := filepath.Rel(build, p)
			bad = append(bad, fmt.Sprintf("%s: %s", rel, m))
		}
		return nil
	})
	if err != nil {
		return harnessErr(fmt.Errorf("website build: %w", err))
	}
	if len(bad) > 0 {
		return fmt.Errorf("website: third-party assets in the build:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}

// firstLoadJS returns the largest gzip size of the JS a built page loads up front and the
// page it belongs to.
func firstLoadJS(build string) (int, string, error) {
	sizes := map[string]int{}
	gz := func(p string) (int, error) {
		if n, ok := sizes[p]; ok {
			return n, nil
		}
		b, err := os.ReadFile(filepath.Join(build, filepath.FromSlash(p)))
		if err != nil {
			return 0, err
		}
		var buf bytes.Buffer
		w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write(b); err != nil {
			return 0, err
		}
		if err := w.Close(); err != nil {
			return 0, err
		}
		sizes[p] = buf.Len()
		return buf.Len(), nil
	}
	worst, worstPage := 0, ""
	err := filepath.WalkDir(build, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".html" {
			return err
		}
		html, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var queue []string
		for _, m := range reJSRef.FindAllSubmatch(html, -1) {
			queue = append(queue, string(m[1]))
		}
		seen := map[string]bool{}
		total := 0
		for len(queue) > 0 {
			js := queue[0]
			queue = queue[1:]
			if seen[js] {
				continue
			}
			seen[js] = true
			n, err := gz(js)
			if err != nil {
				return err
			}
			total += n
			src, err := os.ReadFile(filepath.Join(build, filepath.FromSlash(js)))
			if err != nil {
				return err
			}
			for _, m := range reStaticImp.FindAllSubmatch(src, -1) {
				queue = append(queue, path.Join(path.Dir(js), string(m[1])))
			}
		}
		if total > worst {
			worst = total
			worstPage, _ = filepath.Rel(build, p)
		}
		return nil
	})
	if err != nil {
		return 0, "", harnessErr(fmt.Errorf("website build: %w", err))
	}
	if worstPage == "" {
		return 0, "", fmt.Errorf("website: the build has no pages")
	}
	return worst, worstPage, nil
}
