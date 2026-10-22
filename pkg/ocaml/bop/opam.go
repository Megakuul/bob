package bop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/builder"
)

// opamBuildKind is kind opam-build: one opam package from its own build instructions.
func opamBuildKind(c *builder.Ctx, raw json.RawMessage) error {
	var a OpamBuildArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !filepath.IsAbs(a.Out) || a.Name == "" || strings.ContainsAny(a.Name, "/\x00") || (a.Src != "" && !filepath.IsAbs(a.Src)) {
		return builder.Invalidf("opam-build: out, name and src must be valid")
	}
	for _, d := range []string{a.Out, a.Out + "/lib", a.Out + "/bin", a.Out + "/lib/stublibs"} {
		if err := c.MkdirAll(d); err != nil {
			return err
		}
	}
	if a.Src == "" && len(a.Commands) == 0 {
		return nil // a package without source or commands (conf-*, virtual packages)
	}
	r, err := newRunner(c, a.Path, a.Env)
	if err != nil {
		return err
	}
	root := filepath.Join(c.BuildTop, "src")
	if a.Src != "" {
		c.Phase("unpack")
		if root, err = unpackSource(c, r, a.Src); err != nil {
			return err
		}
	} else if err := c.MkdirAll(root); err != nil {
		return err
	}
	c.Phase("build")
	jobs := strconv.Itoa(cores())
	for i, cmd := range a.Commands {
		if len(cmd) == 0 {
			return builder.Invalidf("opam-build: command %d is empty", i)
		}
		argv := make([]string, len(cmd))
		for j, s := range cmd {
			argv[j] = strings.ReplaceAll(s, JobsMarker, jobs)
		}
		if err := r.run(root, argv...); err != nil {
			return fmt.Errorf("opam:%s: %s: %w", a.Name, strings.Join(argv, " "), err)
		}
	}
	install := filepath.Join(root, a.Name+".install")
	if _, err := os.Stat(install); err == nil {
		c.Phase("install")
		if !filepath.IsAbs(a.Installer) {
			return builder.Invalidf("opam-build: installer %q is not an absolute path", a.Installer)
		}
		err := r.run(root, a.Installer, "--prefix="+a.Out, "--libdir="+a.Out+"/lib",
			"--stubsdir="+a.Out+"/lib/stublibs", a.Name+".install")
		if err != nil {
			return fmt.Errorf("opam:%s: opam-installer: %w", a.Name, err)
		}
	}
	return nil
}

// Magic numbers of compressed archives the builder's own unpacker does not read.
var (
	xzMagic   = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// unpackSource unpacks archive into <build>/src and returns the source root: the archive's
// only top-level directory, as opam does, else the directory itself. tar.xz and tar.zst are
// decompressed with the job's xz/zstd first.
func unpackSource(c *builder.Ctx, r *runner, archive string) (string, error) {
	dst := filepath.Join(c.BuildTop, "src")
	head := make([]byte, 6)
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	n, _ := io.ReadFull(f, head)
	_ = f.Close()
	head = head[:n]
	for _, z := range []struct {
		magic []byte
		prog  string
	}{{xzMagic, "xz"}, {zstdMagic, "zstd"}} {
		if !bytes.HasPrefix(head, z.magic) {
			continue
		}
		tarPath := filepath.Join(c.BuildTop, "source.tar")
		if err := decompress(c, r, z.prog, archive, tarPath); err != nil {
			return "", err
		}
		archive = tarPath
		break
	}
	if err := c.Unpack(archive, dst, 0); err != nil {
		return "", err
	}
	ents, err := os.ReadDir(dst)
	if err != nil {
		return "", err
	}
	if len(ents) == 1 && ents[0].IsDir() {
		return filepath.Join(dst, ents[0].Name()), nil
	}
	return dst, nil
}

// decompress runs prog -dc on from into the writable file to.
func decompress(c *builder.Ctx, r *runner, prog, from, to string) error {
	p, err := lookPath(prog, r.path)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", from, err)
	}
	w, err := c.Writable(to)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(w, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := &exec.Cmd{Path: p, Args: []string{prog, "-dc", from}, Env: r.env, Stdout: out, Stderr: &stderr}
	runErr := cmd.Run()
	if err := out.Close(); err != nil && runErr == nil {
		runErr = err
	}
	if runErr != nil {
		return fmt.Errorf("unpack %s: %s: %v: %s", from, prog, runErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}
