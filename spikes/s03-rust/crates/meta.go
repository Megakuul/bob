package crates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

const maxCrate = 64 << 20 // hostile input: cap one .crate download

// CrateURL is the crates.io download URL.
func CrateURL(name, version string) string {
	return fmt.Sprintf("https://static.crates.io/crates/%s/%s-%s.crate", name, name, version)
}

// Download fetches a .crate into the cache and verifies it against the SRI checksum.
func Download(ctx context.Context, cache string, c *LockedCrate) (string, error) {
	file := filepath.Join(cache, "crates", c.Name+"-"+c.Version+".crate")
	if b, err := os.ReadFile(file); err == nil && sri(b) == c.Checksum {
		return file, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", CrateURL(c.Name, c.Version), nil)
	if err != nil {
		return "", err
	}
	cl := &http.Client{Timeout: 120 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s %s: HTTP %s", c.Name, c.Version, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCrate+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxCrate {
		return "", fmt.Errorf("%s %s: crate larger than %d bytes", c.Name, c.Version, maxCrate)
	}
	if got := sri(b); got != c.Checksum {
		return "", fmt.Errorf("%s %s: download does not match checksum (index %s, got %s)", c.Name, c.Version, c.Checksum, got)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	return file, os.Rename(tmp, file)
}

func sri(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256-" + base64.StdEncoding.EncodeToString(h[:])
}

type manifest struct {
	Package struct {
		Edition string `toml:"edition"`
		Build   any    `toml:"build"`
		Links   string `toml:"links"`
	} `toml:"package"`
	Lib *struct {
		Name       string   `toml:"name"`
		Path       string   `toml:"path"`
		ProcMacro  bool     `toml:"proc-macro"`
		ProcMacro2 bool     `toml:"proc_macro"`
		CrateType  []string `toml:"crate-type"`
	} `toml:"lib"`
}

// ReadMeta extracts CrateMeta from a .crate (gzip tar with prefix name-version/).
func ReadMeta(file, name, version string) (*CrateMeta, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	prefix := name + "-" + version + "/"
	var cargoToml []byte
	files := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rel := strings.TrimPrefix(h.Name, prefix)
		files[rel] = true
		if rel == "Cargo.toml" {
			if cargoToml, err = io.ReadAll(io.LimitReader(tr, 4<<20)); err != nil {
				return nil, err
			}
		}
	}
	if cargoToml == nil {
		return nil, fmt.Errorf("%s %s: no Cargo.toml in crate", name, version)
	}
	var m manifest
	if _, err := toml.NewDecoder(bytes.NewReader(cargoToml)).Decode(&m); err != nil {
		return nil, fmt.Errorf("%s %s: Cargo.toml: %w", name, version, err)
	}
	cm := &CrateMeta{Edition: m.Package.Edition}
	if cm.Edition == "" {
		cm.Edition = "2015"
	}
	switch b := m.Package.Build.(type) {
	case string:
		cm.Build = b
	case bool:
		if b && files["build.rs"] {
			cm.Build = "build.rs"
		}
	case nil:
		if files["build.rs"] {
			cm.Build = "build.rs"
		}
	}
	if m.Lib != nil {
		if m.Lib.Name != "" && m.Lib.Name != strings.ReplaceAll(name, "-", "_") {
			cm.LibName = m.Lib.Name
		}
		if m.Lib.Path != "" && m.Lib.Path != "src/lib.rs" {
			cm.LibPath = m.Lib.Path
		}
		cm.ProcMacro = m.Lib.ProcMacro || m.Lib.ProcMacro2
	}
	if cm.LibPath == "" && !files["src/lib.rs"] {
		return nil, fmt.Errorf("%s %s: no library target (src/lib.rs missing)", name, version)
	}
	return cm, nil
}

// FillMeta downloads every locked crate (parallel) and records its metadata in the lock.
func FillMeta(ctx context.Context, cache string, l *Lock, n int) error {
	ch := make(chan *LockedCrate)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range ch {
				f, err := Download(ctx, cache, c)
				if err == nil {
					c.Meta, err = ReadMeta(f, c.Name, c.Version)
				}
				if err == nil {
					c.NarHash, err = UnpackedNarHash(f)
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}
	for _, c := range l.Crates {
		if c.Meta == nil {
			ch <- c
		}
	}
	close(ch)
	wg.Wait()
	return errors.Join(errs...)
}
