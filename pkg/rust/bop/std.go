package bop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/builder"
)

// RustStdArgs is the args of kind rust-std (SPEC R-FW-034): copy the standard library of a
// target out of that target system's rustc, exactly the files its manifest lists, into
// $out/lib/rustlib/<Triple>/… (a sysroot), and write the manifest with sysroot-relative paths.
type RustStdArgs struct {
	From   string `json:"from"`   // the target system's rustc (unwrapped)
	Triple string `json:"triple"` // "x86_64-unknown-linux-gnu"
}

var tripleRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9_]+(-[a-z0-9_]+){2,3}$`) })

// maxManifest caps the manifest bob reads (the real one is 55 lines).
const maxManifest = 1 << 20

func rustStdKind(c *builder.Ctx, raw json.RawMessage) error {
	var a RustStdArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !filepath.IsAbs(a.From) || filepath.Clean(a.From) != a.From || !tripleRe().MatchString(a.Triple) {
		return builder.Invalidf("rust-std job: from must be a clean absolute path and triple a target triple")
	}
	o, err := out(c)
	if err != nil {
		return err
	}
	c.Phase("rust-std")
	return writeStd(a.From, a.Triple, o)
}

// writeStd copies the standard library of triple from the rustc at from into the sysroot o.
func writeStd(from, triple, o string) error {
	rels, err := stdFiles(from, triple)
	if err != nil {
		return err
	}
	var manifest bytes.Buffer
	for _, rel := range rels {
		if err := copyFile(filepath.Join(from, rel), filepath.Join(o, rel)); err != nil {
			return err
		}
		manifest.WriteString("file:" + rel + "\n")
	}
	return os.WriteFile(filepath.Join(o, "lib", "rustlib", "manifest-rust-std-"+triple), manifest.Bytes(), 0o644)
}

// stdFiles reads lib/rustlib/manifest-rust-std-<triple> of from and returns its files relative
// to from; each must lie in lib/rustlib/<triple>/.
func stdFiles(from, triple string) ([]string, error) {
	name := filepath.Join(from, "lib", "rustlib", "manifest-rust-std-"+triple)
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("the target rustc has no standard library for %s: %w", triple, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxManifest {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxManifest)
	}
	prefix := "lib/rustlib/" + triple + "/"
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		p, ok := strings.CutPrefix(line, "file:")
		if !ok {
			return nil, fmt.Errorf("%s: unsupported entry %q", name, line)
		}
		if rest, abs := strings.CutPrefix(p, from+"/"); abs {
			p = rest
		}
		if path.Clean(p) != p || !strings.HasPrefix(p, prefix) || strings.Contains(p, "..") {
			return nil, fmt.Errorf("%s: entry %q is not below %s", name, p, prefix)
		}
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists no files", name)
	}
	return out, nil
}

// copyFile copies one regular file (no symlinks: the sysroot holds real files).
func copyFile(from, to string) error {
	fi, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", from)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	w, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
