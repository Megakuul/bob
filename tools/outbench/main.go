// Command outbench measures the output budgets of SPEC §4.10 on a prepared repository
// (R-OUT-024 time, R-OUT-025 size; budget rows B-086 and B-087):
//
//   - OCI cold: `bob build <image>` with the programs built but no layer or image built yet;
//   - OCI incremental: one string of the program changed (-edit), the program rebuilt, then
//     `bob build <image>` again; the share of new compressed bytes v1 → v2 is printed;
//   - OCI size: compressed size of the host platform's image (layers + config);
//   - AppImage (-appimage): build time and file size;
//   - closure (-program): NAR size and path count of the program's closure.
//
// The repository is a git working tree after `bob lock` whose programs have been built once in
// the same bob home (toolchains and closures present). The fixture of the budgets is
// tests/fixtures/repos/polyglot (//deploy:api-image, //deploy:api-appimage, //cmd/api:api,
// -edit 'cmd/api/main.go:"hello ":"hullo "'). It prints "BUDGET B-086 <seconds>" (OCI cold) and,
// with -appimage, "BUDGET B-087 <MB>" (AppImage size), plus informational lines. Load averages
// are printed: timings on a loaded host are not comparable with the quiet [8c/15G] profile.
//
//	go run ./tools/outbench -bob /path/to/bob -dir /path/to/repo -home $BOB_HOME
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	bob := flag.String("bob", "bob", "bob executable")
	dir := flag.String("dir", ".", "prepared repository")
	home := flag.String("home", os.Getenv("BOB_HOME"), "bob home of the builds (for store paths and nix path-info)")
	image := flag.String("image", "//deploy:api-image", "oci.Image label")
	program := flag.String("program", "//cmd/api:api", "program label (closure size)")
	appimage := flag.String("appimage", "", "appimage.Bundle label (optional)")
	edit := flag.String("edit", `cmd/api/main.go:"hello ":"hullo "`, "file:old:new, the one-string change of the incremental run")
	flag.Parse()
	if err := run(*bob, *dir, *home, *image, *program, *appimage, *edit); err != nil {
		fmt.Fprintln(os.Stderr, "outbench:", err)
		os.Exit(1)
	}
}

type bench struct {
	bob, dir, home string
}

// build runs `bob build --print-paths --json label` and returns the wall time, the host path of
// the label's first output and counts.built.
func (b bench) build(label string) (time.Duration, string, int, error) {
	start := time.Now()
	cmd := exec.Command(b.bob, "build", "--print-paths", "--json", label)
	cmd.Dir = b.dir
	cmd.Env = append(os.Environ(), "BOB_HOME="+b.home)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	took := time.Since(start)
	if err != nil {
		return took, "", 0, fmt.Errorf("bob build %s: %w\n%s%s", label, err, out.Bytes(), errb.Bytes())
	}
	var path string
	built := 0
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct {
			Event  string         `json:"event"`
			Label  string         `json:"label"`
			Paths  []string       `json:"paths"`
			Counts map[string]int `json:"counts"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch {
		case e.Event == "package.finish" && e.Label == label && len(e.Paths) > 0:
			path = e.Paths[0]
		case e.Event == "run.finish":
			built = e.Counts["built"]
		}
	}
	if path == "" {
		return took, "", built, fmt.Errorf("bob build %s printed no output path", label)
	}
	return took, path, built, nil
}

// layers returns the compressed size of each layer (digest → size) and of the config of the
// host platform's manifest in the OCI layout dir.
func layers(dir string) (map[string]int64, int64, error) {
	read := func(digest string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")))
	}
	type desc struct {
		Digest   string `json:"digest"`
		Size     int64  `json:"size"`
		Platform *struct {
			Architecture string `json:"architecture"`
		} `json:"platform"`
	}
	var top, idx struct{ Manifests []desc }
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil || json.Unmarshal(b, &top) != nil || len(top.Manifests) != 1 {
		return nil, 0, fmt.Errorf("%s: no image index (%v)", dir, err)
	}
	if b, err = read(top.Manifests[0].Digest); err != nil || json.Unmarshal(b, &idx) != nil {
		return nil, 0, fmt.Errorf("%s: image index: %v", dir, err)
	}
	for _, m := range idx.Manifests {
		if m.Platform == nil || m.Platform.Architecture != runtime.GOARCH {
			continue
		}
		var man struct {
			Config desc   `json:"config"`
			Layers []desc `json:"layers"`
		}
		if b, err = read(m.Digest); err != nil || json.Unmarshal(b, &man) != nil {
			return nil, 0, fmt.Errorf("%s: manifest: %v", dir, err)
		}
		out := map[string]int64{}
		for _, l := range man.Layers {
			out[l.Digest] = l.Size
		}
		return out, man.Config.Size, nil
	}
	return nil, 0, fmt.Errorf("%s: no %s manifest", dir, runtime.GOARCH)
}

func total(ls map[string]int64) int64 {
	var n int64
	for _, s := range ls {
		n += s
	}
	return n
}

func loadavg() string {
	b, _ := os.ReadFile("/proc/loadavg")
	return strings.Join(strings.Fields(string(b))[:min(3, len(strings.Fields(string(b))))], " ")
}

func (b bench) git(args ...string) error {
	cmd := exec.Command("git", append([]string{"-c", "user.name=outbench", "-c", "user.email=outbench@bob.invalid"}, args...)...)
	cmd.Dir = b.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}

// change applies a file:old:new edit and commits it; undo restores the file.
func (b bench) change(edit string) (undo func() error, err error) {
	file, rest, ok := strings.Cut(edit, ":")
	old, repl, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 {
		return nil, fmt.Errorf("-edit %q is not file:old:new", edit)
	}
	p := filepath.Join(b.dir, file)
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(src, []byte(old)) {
		return nil, fmt.Errorf("%s does not contain %s", file, old)
	}
	if err := os.WriteFile(p, bytes.Replace(src, []byte(old), []byte(repl), 1), 0o644); err != nil {
		return nil, err
	}
	if err := b.git("commit", "-q", "-am", "outbench v2"); err != nil {
		return nil, err
	}
	return func() error {
		if err := os.WriteFile(p, src, 0o644); err != nil {
			return err
		}
		return b.git("commit", "-q", "-am", "outbench v1")
	}, nil
}

// closure returns the NAR size (bytes) and path count of a store path's closure.
func (b bench) closure(hostPath string) (int64, int, error) {
	nixes, _ := filepath.Glob(filepath.Join(b.home, "bin", "nix-*", "bin", "nix"))
	if len(nixes) == 0 {
		return 0, 0, errors.New("no nix below the bob home")
	}
	cmd := exec.Command(nixes[0], "--extra-experimental-features", "nix-command", "--store", b.home,
		"path-info", "--json", "--json-format", "1", "-r", "/nix/store/"+filepath.Base(hostPath))
	cmd.Env = append(os.Environ(), "HOME="+os.TempDir())
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("nix path-info: %w", err)
	}
	var info map[string]struct {
		NarSize int64 `json:"narSize"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, 0, err
	}
	var n int64
	for _, i := range info {
		n += i.NarSize
	}
	return n, len(info), nil
}

func run(bobExe, dir, home, image, program, appimage, edit string) error {
	if home == "" {
		return errors.New("-home (or BOB_HOME) is required")
	}
	b := bench{bob: bobExe, dir: dir, home: home}
	fmt.Printf("# load before: %s\n", loadavg())
	if _, _, _, err := b.build(program); err != nil {
		return err
	}
	cold, out1, built, err := b.build(image)
	if err != nil {
		return err
	}
	if built == 0 {
		fmt.Println("# warning: the image was already built; the cold time is a no-op time")
	}
	fmt.Printf("BUDGET B-086 %.2f\n", cold.Seconds())
	l1, cfg, err := layers(out1)
	if err != nil {
		return err
	}
	fmt.Printf("# oci cold %.2f s (%d derivations built), %s image compressed %.2f MB in %d layers\n",
		cold.Seconds(), built, runtime.GOARCH, float64(total(l1)+cfg)/1e6, len(l1))

	undo, err := b.change(edit)
	if err != nil {
		return err
	}
	defer func() {
		if err := undo(); err != nil {
			fmt.Fprintln(os.Stderr, "outbench: restoring the source:", err)
		}
	}()
	if _, _, _, err := b.build(program); err != nil {
		return err
	}
	incr, out2, built2, err := b.build(image)
	if err != nil {
		return err
	}
	l2, _, err := layers(out2)
	if err != nil {
		return err
	}
	var fresh int64
	for d, s := range l2 {
		if _, ok := l1[d]; !ok {
			fresh += s
		}
	}
	fmt.Printf("# oci incremental %.2f s (%d derivations built), new compressed bytes %.1f %%\n",
		incr.Seconds(), built2, 100*float64(fresh)/float64(max(total(l2), 1)))

	if size, n, err := b.closure(out1); err == nil {
		fmt.Printf("# image closure %d paths, %.1f MiB\n", n, float64(size)/(1<<20))
	}
	if _, pout, _, err := b.build(program); err == nil {
		if size, n, err := b.closure(pout); err == nil {
			fmt.Printf("# program closure %d paths, %.1f MiB\n", n, float64(size)/(1<<20))
		}
	}
	if appimage != "" {
		took, aout, _, err := b.build(appimage)
		if err != nil {
			return err
		}
		files, _ := filepath.Glob(filepath.Join(aout, "*.AppImage"))
		if len(files) != 1 {
			return fmt.Errorf("%s: want one AppImage, found %v", aout, files)
		}
		st, err := os.Stat(files[0])
		if err != nil {
			return err
		}
		fmt.Printf("BUDGET B-087 %.2f\n", float64(st.Size())/1e6)
		fmt.Printf("# appimage %.2f s, %.2f MB\n", took.Seconds(), float64(st.Size())/1e6)
	}
	fmt.Printf("# load after: %s\n", loadavg())
	return nil
}
